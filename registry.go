package detectharness

import "path/filepath"

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
}

func available(path string) pathResolution { return pathResolution{path: path} }

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
	default:
		return pathResolution{reason: "Claude Desktop has no supported Linux config path"}
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

func rooConfig(_ hostSystem, environment runtimeEnvironment) pathResolution {
	parts := []string{"Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "mcp_settings.json"}
	switch environment.platform {
	case "darwin":
		return available(environment.appSupport(parts...))
	case "windows":
		return joinResolution(environment.appData(), parts...)
	default:
		return joinResolution(environment.xdgConfig(), parts...)
	}
}

func openCodeDirectory(environment runtimeEnvironment) pathResolution {
	if environment.platform == "windows" {
		return joinResolution(environment.appData(), "opencode")
	}
	return joinResolution(environment.xdgConfig(), "opencode")
}

func openCodeConfig(system hostSystem, environment runtimeEnvironment) pathResolution {
	directory := openCodeDirectory(environment)
	if directory.reason != "" {
		return directory
	}
	jsonc := filepath.Join(directory.path, "opencode.jsonc")
	json := filepath.Join(directory.path, "opencode.json")
	jsoncState := probePath(system, jsonc)
	jsonState := probePath(system, json)
	if jsoncState.state == Detected && jsonState.state == Detected {
		return pathResolution{reason: "both opencode.jsonc and opencode.json exist; the authoritative config is ambiguous"}
	}
	if jsoncState.state == Detected {
		return available(jsonc)
	}
	if jsoncState.state == Unavailable {
		return pathResolution{reason: jsoncState.reason}
	}
	if jsonState.state == Unavailable {
		return pathResolution{reason: jsonState.reason}
	}
	return available(json)
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
		Harness: Harness{ID: ClaudeCode, Name: "Claude Code", ReloadHint: "restart Claude Code sessions"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution { return available(r.home(".claude.json")) },
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "claude"), probePath(s, r.home(".claude.json")), probePath(s, r.home(".claude")))
		},
	},
	{
		Harness: Harness{ID: Cursor, Name: "Cursor", ReloadHint: "restart Cursor"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".cursor", "mcp.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probePath(s, r.home(".cursor")), appBundle(s, r, "Cursor.app"), windowsPath(s, r, r.localAppData(), "Programs", "cursor"))
		},
	},
	{
		Harness: Harness{ID: Codex, Name: "Codex CLI", ReloadHint: "restart Codex sessions"},
		format:  formatTOML, entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".codex", "config.toml"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "codex"), probePath(s, r.home(".codex")))
		},
	},
	{
		Harness: Harness{ID: GeminiCLI, Name: "Gemini CLI", ReloadHint: "restart Gemini CLI sessions"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".gemini", "settings.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "gemini"), probePath(s, r.home(".gemini")))
		},
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
		Harness: Harness{ID: Zed, Name: "Zed", ReloadHint: "Zed reloads settings automatically"},
		format:  formatJSONC, topKey: "context_servers", entry: entryStandard, config: zedConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "zed"), resolvedProbe(s, zedConfig(s, r)), appBundle(s, r, "Zed.app"))
		},
	},
	{
		Harness: Harness{ID: Cline, Name: "Cline", ReloadHint: "restart the server in Cline's MCP panel"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".cline", "data", "settings", "cline_mcp_settings.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probePath(s, r.home(".cline")), probeExtension(s, r, "saoudrizwan.claude-dev-"))
		},
	},
	{
		Harness: Harness{ID: RooCode, Name: "Roo Code", ReloadHint: "restart the server in Roo's MCP panel"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard, config: rooConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(resolvedProbe(s, rooConfig(s, r)), probeExtension(s, r, "rooveterinaryinc.roo-cline-"))
		},
	},
	{
		Harness: Harness{ID: AmazonQ, Name: "Amazon Q Developer CLI", ReloadHint: "restart Amazon Q sessions"},
		format:  formatJSON, topKey: "mcpServers", entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".aws", "amazonq", "mcp.json"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "q"), probeCommand(s, r, "qchat"), probePath(s, r.home(".aws", "amazonq")))
		},
	},
	{
		Harness: Harness{ID: Continue, Name: "Continue", ReloadHint: "reload Continue config"},
		format:  formatYAMLList, entry: entryStandard,
		config: func(_ hostSystem, r runtimeEnvironment) pathResolution {
			return available(r.home(".continue", "config.yaml"))
		},
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probePath(s, r.home(".continue")), probeExtension(s, r, "continue.continue-"))
		},
	},
	{
		Harness: Harness{ID: OpenCode, Name: "OpenCode", ReloadHint: "restart OpenCode"},
		format:  formatJSONC, topKey: "mcp", entry: entryOpenCode, config: openCodeConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "opencode"), resolvedProbe(s, openCodeDirectory(r)))
		},
	},
	{
		Harness: Harness{ID: VSCode, Name: "VS Code", ReloadHint: "reload VS Code or restart Copilot chat"},
		format:  formatJSONC, topKey: "servers", entry: entryVSCode, config: vscodeConfig,
		detect: func(s hostSystem, r runtimeEnvironment) probeResult {
			return combineProbes(probeCommand(s, r, "code"), resolvedProbe(s, vscodeConfig(s, r)), appBundle(s, r, "Visual Studio Code.app"), windowsPath(s, r, r.localAppData(), "Programs", "Microsoft VS Code"))
		},
	},
}

var registryByID = func() map[ID]harnessDefinition {
	result := make(map[ID]harnessDefinition, len(registry))
	for _, definition := range registry {
		result[definition.ID] = definition
	}
	return result
}()

// Supported returns the ordered built-in harness catalog.
func Supported() []Harness {
	result := make([]Harness, len(registry))
	for index, definition := range registry {
		result[index] = definition.Harness
	}
	return result
}

// IsSupported reports whether id identifies a built-in harness.
func IsSupported(id ID) bool {
	_, found := registryByID[id]
	return found
}
