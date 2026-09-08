package detectharness

import (
	"path/filepath"
	"strings"
)

type configFormat string

const (
	formatJSON     configFormat = "json"
	formatJSONC    configFormat = "jsonc"
	formatTOML     configFormat = "toml"
	formatYAMLList configFormat = "yaml-list"
)

type entryKind string

const (
	entryStandard entryKind = "standard"
	entryOpenCode entryKind = "opencode"
	entryVSCode   entryKind = "vscode"
)

type harnessDefinition struct {
	Harness
	format configFormat
	topKey string
	entry  entryKind
	config func(hostSystem, runtimeEnvironment) pathResolution
	detect func(hostSystem, runtimeEnvironment) probeResult
	// project resolves a directory-local configuration path. A nil project
	// means the harness only supports a global configuration.
	project *projectDefinition
	// layers lists the other configuration sources the harness merges with the
	// managed file in the same scope. Plan inspects them for same-name entries
	// that would shadow or outlive the managed entry. A nil layers means the
	// managed file is the only source for its scope.
	layers func(scope Scope, target string, system hostSystem, environment runtimeEnvironment) []configLayer
}

// projectDefinition holds the resolver for project-scoped configuration.
type projectDefinition struct {
	resolve func(dir string, system hostSystem, environment runtimeEnvironment) pathResolution
}

// configLayer is one additional configuration source a harness merges with the
// managed file. Either path (a file) or raw (inline content) is set.
type configLayer struct {
	// name identifies the layer in messages: a file path or an environment
	// variable name.
	name string
	// path is the file to inspect. It is empty for inline layers.
	path string
	// raw is inline content for layers that are not files.
	raw []byte
	// overrides reports whether the layer is merged after the managed file and
	// therefore wins on conflicting fields.
	overrides bool
}

func available(path string) pathResolution { return pathResolution{path: path} }

// resolveConfig resolves the configuration path for the active scope. Project
// scope returns a reason (not an error) for harnesses without project support.
func (d harnessDefinition) resolveConfig(scope Scope, system hostSystem, environment runtimeEnvironment) pathResolution {
	if scope.Mode == ScopeProject {
		if d.project == nil {
			return pathResolution{reason: d.Name + " has no project-scope configuration"}
		}
		return d.project.resolve(scope.Dir, system, environment)
	}
	return d.config(system, environment)
}

// resolveLayers lists the additional configuration sources for the scope, or
// nil when the harness has none.
func (d harnessDefinition) resolveLayers(scope Scope, target string, system hostSystem, environment runtimeEnvironment) []configLayer {
	if d.layers == nil {
		return nil
	}
	return d.layers(scope, target, system, environment)
}

// projectFile returns a resolver that joins the project directory with rel.
func projectFile(rel string) func(dir string, system hostSystem, environment runtimeEnvironment) pathResolution {
	return func(dir string, _ hostSystem, _ runtimeEnvironment) pathResolution {
		return available(filepath.Join(dir, rel))
	}
}

func joinResolution(root pathResolution, parts ...string) pathResolution {
	if root.reason != "" {
		return root
	}
	all := append([]string{root.path}, parts...)
	return available(filepath.Join(all...))
}

func claudeDesktopConfig(_ hostSystem, environment runtimeEnvironment) pathResolution {
	switch environment.platform {
	case "darwin":
		return available(environment.appSupport("Claude", "claude_desktop_config.json"))
	case "windows":
		return joinResolution(environment.appData(), "Claude", "claude_desktop_config.json")
	case "linux":
		return joinResolution(environment.xdgConfig(), "Claude", "claude_desktop_config.json")
	default:
		return pathResolution{reason: "Claude Desktop is not supported on this platform"}
	}
}

func zedConfig(_ hostSystem, environment runtimeEnvironment) pathResolution {
	switch environment.platform {
	case "darwin":
		return available(environment.appSupport("zed", "settings.json"))
	case "windows":
		return joinResolution(environment.appData(), "Zed", "settings.json")
	default:
		return joinResolution(environment.xdgConfig(), "zed", "settings.json")
	}
}

func zooConfig(_ hostSystem, environment runtimeEnvironment) pathResolution {
	parts := []string{"Code", "User", "globalStorage", "ZooCodeOrganization.zoo-code", "mcp_settings.json"}
	switch environment.platform {
	case "darwin":
		return available(environment.appSupport(parts...))
	case "windows":
		return joinResolution(environment.appData(), parts...)
	default:
		return joinResolution(environment.xdgConfig(), parts...)
	}
}

func clineConfig(_ hostSystem, environment runtimeEnvironment) pathResolution {
	parts := []string{"Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings", "cline_mcp_settings.json"}
	switch environment.platform {
	case "darwin":
		return available(environment.appSupport(parts...))
	case "windows":
		return joinResolution(environment.appData(), parts...)
	default:
		return joinResolution(environment.xdgConfig(), parts...)
	}
}

// OpenCode configuration layout, verified against anomalyco/opencode
// (packages/core/src/global.ts, packages/opencode/src/config/{config,paths}.ts).
//
// The global directory is <xdg config>/opencode on every platform, including
// Windows, because OpenCode resolves it with the xdg-basedir convention rather
// than %APPDATA%. Within one directory OpenCode deep-merges, in order,
// config.json (legacy), opencode.json, and opencode.jsonc, so a later file
// wins on conflicting fields. On first run without any of them OpenCode seeds
// opencode.jsonc, which is therefore the file this library creates and
// prefers.
//
// The full merge order, lowest precedence first, is: global files,
// $OPENCODE_CONFIG, project files walked from the git root to the working
// directory (opencode.json then opencode.jsonc per directory), every
// .opencode/ directory on the same walk, ~/.opencode/, $OPENCODE_CONFIG_DIR,
// and finally $OPENCODE_CONFIG_CONTENT.
const (
	openCodeLegacyFile   = "config.json"
	openCodeStandardFile = "opencode.json"
	openCodeHumanFile    = "opencode.jsonc"
	openCodeLocalDir     = ".opencode"
)

func openCodeDirectory(environment runtimeEnvironment) pathResolution {
	return joinResolution(environment.xdgConfig(), "opencode")
}

// openCodeConfigIn resolves the managed OpenCode file inside dir. An existing
// opencode.jsonc always wins because OpenCode merges it last; opencode.json is
// used only when it is the sole file present; a fresh directory receives
// opencode.jsonc to match OpenCode's own default.
func openCodeConfigIn(dir string, system hostSystem) pathResolution {
	human := filepath.Join(dir, openCodeHumanFile)
	standard := filepath.Join(dir, openCodeStandardFile)
	humanState := probePath(system, human)
	if humanState.state == Detected {
		return available(human)
	}
	if humanState.state == Unavailable {
		return pathResolution{reason: humanState.reason}
	}
	standardState := probePath(system, standard)
	if standardState.state == Detected {
		return available(standard)
	}
	if standardState.state == Unavailable {
		return pathResolution{reason: standardState.reason}
	}
	return available(human)
}

func openCodeConfig(system hostSystem, environment runtimeEnvironment) pathResolution {
	directory := openCodeDirectory(environment)
	if directory.reason != "" {
		return directory
	}
	return openCodeConfigIn(directory.path, system)
}

func openCodeProjectConfig(dir string, system hostSystem, _ runtimeEnvironment) pathResolution {
	return openCodeConfigIn(dir, system)
}

// openCodeLayers lists the other configuration sources OpenCode merges with the
// managed file in the same scope. Global-scope layers are not reported for
// project scope (and vice versa) because differing per-project settings are
// the purpose of project scope, not a conflict.
func openCodeLayers(scope Scope, target string, _ hostSystem, environment runtimeEnvironment) []configLayer {
	var layers []configLayer
	file := func(path string, overrides bool) {
		layers = append(layers, configLayer{name: path, path: path, overrides: overrides})
	}
	directoryFiles := func(dir string, overrides bool) {
		file(filepath.Join(dir, openCodeStandardFile), overrides)
		file(filepath.Join(dir, openCodeHumanFile), overrides)
	}
	targetDir := filepath.Dir(target)
	if scope.Mode == ScopeProject {
		if filepath.Base(target) == openCodeHumanFile {
			file(filepath.Join(targetDir, openCodeStandardFile), false)
		}
		directoryFiles(filepath.Join(targetDir, openCodeLocalDir), true)
	} else {
		file(filepath.Join(targetDir, openCodeLegacyFile), false)
		if filepath.Base(target) == openCodeHumanFile {
			file(filepath.Join(targetDir, openCodeStandardFile), false)
		}
		if custom, _ := environment.lookupEnv("OPENCODE_CONFIG"); strings.TrimSpace(custom) != "" {
			layers = append(layers, configLayer{name: "$OPENCODE_CONFIG (" + custom + ")", path: custom, overrides: true})
		}
	}
	directoryFiles(environment.home(openCodeLocalDir), true)
	if custom, _ := environment.lookupEnv("OPENCODE_CONFIG_DIR"); strings.TrimSpace(custom) != "" {
		for _, name := range []string{openCodeStandardFile, openCodeHumanFile} {
			path := filepath.Join(custom, name)
			layers = append(layers, configLayer{name: "$OPENCODE_CONFIG_DIR (" + path + ")", path: path, overrides: true})
		}
	}
	if content, _ := environment.lookupEnv("OPENCODE_CONFIG_CONTENT"); strings.TrimSpace(content) != "" {
		layers = append(layers, configLayer{name: "$OPENCODE_CONFIG_CONTENT", raw: []byte(content), overrides: true})
	}
	return layers
}

// openCodeGlobalEvidence probes every file OpenCode reads from its global
// directory so detection evidence names the files that actually exist.
func openCodeGlobalEvidence(system hostSystem, environment runtimeEnvironment) probeResult {
	directory := openCodeDirectory(environment)
	if directory.reason != "" {
		return probeResult{state: Unavailable, reason: directory.reason}
	}
	results := []probeResult{probePath(system, directory.path)}
	for _, name := range []string{openCodeLegacyFile, openCodeStandardFile, openCodeHumanFile} {
		results = append(results, probePath(system, filepath.Join(directory.path, name)))
	}
	if custom, _ := environment.lookupEnv("OPENCODE_CONFIG"); strings.TrimSpace(custom) != "" {
		results = append(results, probePath(system, custom))
	}
	return combineProbes(results...)
}

func vscodeConfig(_ hostSystem, environment runtimeEnvironment) pathResolution {
	switch environment.platform {
	case "darwin":
		return available(environment.appSupport("Code", "User", "mcp.json"))
	case "windows":
		return joinResolution(environment.appData(), "Code", "User", "mcp.json")
	default:
		return joinResolution(environment.xdgConfig(), "Code", "User", "mcp.json")
	}
}

func resolvedProbe(system hostSystem, resolution pathResolution) probeResult {
	if resolution.reason != "" {
		return probeResult{state: Unavailable, reason: resolution.reason}
	}
	return probePath(system, resolution.path)
}

func appBundle(system hostSystem, environment runtimeEnvironment, name string) probeResult {
	if environment.platform != "darwin" {
		return probeResult{state: NotDetected}
	}
	return probePath(system, filepath.Join("/Applications", name))
}

func windowsPath(system hostSystem, environment runtimeEnvironment, root pathResolution, parts ...string) probeResult {
	if environment.platform != "windows" {
		return probeResult{state: NotDetected}
	}
	return resolvedProbe(system, joinResolution(root, parts...))
}

var registry = []harnessDefinition{
	{
		Harness: Harness{ID: ClaudeDesktop, Name: "Claude Desktop", ReloadHint: "quit and restart Claude Desktop"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard, config: claudeDesktopConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(resolvedProbe(s, claudeDesktopConfig(s, r)), appBundle(s, r, "Claude.app"), windowsPath(s, r, r.localAppData(), "AnthropicClaude"))
		},
	},
	{
		Harness: Harness{
			ID: ClaudeCode, Name: "Claude Code", ReloadHint: "restart Claude Code sessions",
			Project: &ProjectScope{
				Path:       ".mcp.json",
				ReloadHint: "restart Claude Code sessions",
				Lifecycle:  "Created manually or by `claude mcp add --scope project`. Safe to pre-create; Claude Code reads it on session start and never overwrites it. Project servers require per-session approval on first use.",
				Shareable:  true,
				TrustGate:  true,
			},
		},
		format: formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution { return available(r.home(".claude.json")) },
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "claude"), probePath(s, r.home(".claude.json")), probePath(s, r.home(".claude")))
		},
		project: &projectDefinition{resolve: projectFile(".mcp.json")},
	},
	{
		Harness: Harness{
			ID: Cursor, Name: "Cursor", ReloadHint: "restart Cursor",
			Project: &ProjectScope{
				Path:       ".cursor/mcp.json",
				ReloadHint: "restart Cursor",
				Lifecycle:  "Created manually, via the MCP tools UI, or `cursor mcp add`. Safe to pre-create; Cursor reads it on startup and never overwrites it.",
				Shareable:  true,
			},
		},
		format: formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".cursor", "mcp.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probePath(s, r.home(".cursor")), appBundle(s, r, "Cursor.app"), windowsPath(s, r, r.localAppData(), "Programs", "cursor"))
		},
		project: &projectDefinition{resolve: projectFile(".cursor/mcp.json")},
	},
	{
		Harness: Harness{
			ID: Codex, Name: "Codex CLI", ReloadHint: "restart Codex sessions",
			Project: &ProjectScope{
				Path:       ".codex/config.toml",
				ReloadHint: "restart Codex sessions",
				Lifecycle:  "Read-only by Codex; safe to pre-create. Loaded only after the project is trusted. Codex walks .codex/config.toml from the project root to the working directory and deep-merges every layer.",
				Shareable:  true,
				TrustGate:  true,
			},
		},
		format: formatTOML, entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".codex", "config.toml"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "codex"), probePath(s, r.home(".codex")))
		},
		project: &projectDefinition{resolve: projectFile(".codex/config.toml")},
	},
	{
		Harness: Harness{
			ID: GeminiCLI, Name: "Gemini CLI", ReloadHint: "restart Gemini CLI sessions",
			Project: &ProjectScope{
				Path:       ".gemini/settings.json",
				ReloadHint: "run `/mcp reload`, or restart Gemini CLI sessions",
				Lifecycle:  "Created manually or via `gemini mcp add`. Safe to pre-create; Gemini reads it on load and never overwrites it. Applied after workspace trust.",
				Shareable:  true,
				TrustGate:  true,
			},
		},
		format: formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".gemini", "settings.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "gemini"), probePath(s, r.home(".gemini")))
		},
		project: &projectDefinition{resolve: projectFile(".gemini/settings.json")},
	},
	{
		Harness: Harness{ID: Windsurf, Name: "Windsurf", ReloadHint: "Windsurf reloads the affected server automatically"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".codeium", "windsurf", "mcp_config.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probePath(s, r.home(".codeium", "windsurf")), appBundle(s, r, "Windsurf.app"), windowsPath(s, r, r.localAppData(), "Programs", "Windsurf"))
		},
	},
	{
		Harness: Harness{
			ID: Zed, Name: "Zed", ReloadHint: "Zed reloads settings automatically",
			Project: &ProjectScope{
				Path:       ".zed/settings.json",
				ReloadHint: "Zed reloads settings automatically",
				Lifecycle:  "Created manually or via the AI settings UI. Safe to pre-create; Zed watches the file and applies changes live. A separate worktree-trust prompt may appear on first open.",
				Shareable:  true,
			},
		},
		format: formatJSONC, topKey: "context_servers", entry: entryStandard, config: zedConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "zed"), resolvedProbe(s, zedConfig(s, r)), appBundle(s, r, "Zed.app"))
		},
		project: &projectDefinition{resolve: projectFile(".zed/settings.json")},
	},
	{
		Harness: Harness{ID: Cline, Name: "Cline", ReloadHint: "restart the server in Cline's MCP panel"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard, config: clineConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(resolvedProbe(s, clineConfig(s, r)), probeExtension(s, r, "saoudrizwan.claude-dev-"), probePath(s, r.home(".cline", "mcp.json")))
		},
	},
	{
		Harness: Harness{
			ID: ZooCode, Name: "Zoo Code", ReloadHint: "restart the server in Zoo Code's MCP panel",
			Project: &ProjectScope{
				Path:       ".roo/mcp.json",
				ReloadHint: "Zoo Code hot-reloads the file automatically",
				Lifecycle:  "Created manually or via Zoo Code's MCP panel. Safe to pre-create; Zoo Code watches the file and hot-reloads. Zoo Code continues the discontinued Roo Code extension and keeps the .roo/mcp.json convention.",
				Shareable:  true,
			},
		},
		format: formatJSON, topKey: "mcpServers", entry: entryStandard, config: zooConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(resolvedProbe(s, zooConfig(s, r)), probeExtension(s, r, "ZooCodeOrganization.zoo-code-"))
		},
		project: &projectDefinition{resolve: projectFile(".roo/mcp.json")},
	},
	{
		Harness: Harness{
			ID: AmazonQ, Name: "Amazon Q Developer CLI", ReloadHint: "restart Amazon Q sessions",
			Project: &ProjectScope{
				Path:       ".amazonq/mcp.json",
				ReloadHint: "restart Amazon Q sessions",
				Lifecycle:  "Created by `q mcp add --scope workspace` or manually. Safe to pre-create; Amazon Q reads it and never overwrites an existing file.",
				Shareable:  true,
			},
		},
		format: formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".aws", "amazonq", "mcp.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "q"), probeCommand(s, r, "qchat"), probePath(s, r.home(".aws", "amazonq")), probePath(s, r.home(".aws", "amazonq", "cli-agents")))
		},
		project: &projectDefinition{resolve: projectFile(".amazonq/mcp.json")},
	},
	{
		Harness: Harness{
			ID: Continue, Name: "Continue", ReloadHint: "reload Continue config",
			Project: &ProjectScope{
				Path:       ".continue/mcpServers/detect-harness.yaml",
				ReloadHint: "Continue reloads config on save",
				Lifecycle:  "A YAML block file inside .continue/mcpServers/. Safe to pre-create; Continue concatenates every block file in that directory and dedupes by server name.",
				Shareable:  true,
			},
		},
		format: formatYAMLList, entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".continue", "config.yaml"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probePath(s, r.home(".continue")), probeExtension(s, r, "continue.continue-"))
		},
		project: &projectDefinition{resolve: projectFile(filepath.Join(".continue", "mcpServers", "detect-harness.yaml"))},
	},
	{
		Harness: Harness{
			ID: OpenCode, Name: "OpenCode", ReloadHint: "restart OpenCode",
			Project: &ProjectScope{
				Path:       "opencode.jsonc",
				ReloadHint: "restart OpenCode",
				Lifecycle:  "opencode.jsonc at the project root (an existing opencode.json is used only when it is the sole file; opencode.jsonc wins when both exist). Safe to pre-create; OpenCode reads and deep-merges it with global config, walking from the git root to the working directory, then applies any .opencode/ directory on the same walk. It never overwrites an existing file. Ignored when OPENCODE_DISABLE_PROJECT_CONFIG is set.",
				Shareable:  true,
			},
		},
		format: formatJSONC, topKey: "mcp", entry: entryOpenCode, config: openCodeConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "opencode"), openCodeGlobalEvidence(s, r))
		},
		project: &projectDefinition{resolve: openCodeProjectConfig},
		layers:  openCodeLayers,
	},
	{
		Harness: Harness{
			ID: VSCode, Name: "VS Code", ReloadHint: "reload VS Code or restart Copilot chat",
			Project: &ProjectScope{
				Path:       ".vscode/mcp.json",
				ReloadHint: "reload the server via MCP: List Servers, or reload VS Code",
				Lifecycle:  "Created via MCP: Add Server or manually. Safe to pre-create; VS Code reads it and never overwrites it. First server start shows a trust dialog.",
				Shareable:  true,
				TrustGate:  true,
			},
		},
		format: formatJSONC, topKey: "servers", entry: entryVSCode, config: vscodeConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "code"), resolvedProbe(s, vscodeConfig(s, r)), appBundle(s, r, "Visual Studio Code.app"), windowsPath(s, r, r.localAppData(), "Programs", "Microsoft VS Code"))
		},
		project: &projectDefinition{resolve: projectFile(".vscode/mcp.json")},
	},
}

var registryByID = func() map[ID]harnessDefinition {
	result := make(map[ID]harnessDefinition, len(registry))
	for _, definition := range registry {
		result[definition.ID] = definition
	}
	return result
}()

// idAliases maps deprecated harness ids to their canonical replacement. Input
// ids are resolved through this map before lookup; output always uses the
// canonical id.
var idAliases = map[ID]ID{
	RooCode: ZooCode,
}

// CanonicalID resolves deprecated harness ids (for example "roo-code") to their
// canonical replacement. It is exported so protocol-level callers can deduplicate
// ids consistently with the registry.
func CanonicalID(id ID) ID {
	if canonical, found := idAliases[id]; found {
		return canonical
	}
	return id
}

func definitionFor(id ID) (harnessDefinition, bool) {
	definition, found := registryByID[CanonicalID(id)]
	return definition, found
}

// Supported returns the ordered built-in harness catalog.
func Supported() []Harness {
	result := make([]Harness, len(registry))
	for index, definition := range registry {
		result[index] = definition.Harness
	}
	return result
}

// IsSupported reports whether id identifies a built-in harness. Deprecated
// aliases (for example "roo-code") are accepted and resolve to their canonical
// harness.
func IsSupported(id ID) bool {
	_, found := definitionFor(id)
	return found
}
