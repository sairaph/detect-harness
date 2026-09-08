package detectharness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installerWithEnv builds a Linux installer whose XDG configuration root lives
// under a temporary home, with extra environment variables layered on top.
func installerWithEnv(t *testing.T, extra map[string]string) (*Installer, string) {
	t.Helper()
	home := t.TempDir()
	env := map[string]string{
		"PATH":            "",
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
	}
	for key, value := range extra {
		env[key] = value
	}
	installer, err := New(testServer(), WithEnvironment(DetectOptions{Platform: "linux", HomeDir: home, Env: env}))
	if err != nil {
		t.Fatal(err)
	}
	return installer, home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openCodeEntry(command string) string {
	return `{"mcp":{"example.mcp":{"type":"local","command":["` + command + `"]}}}`
}

// openCodeOwnedEntry is the exact entry the test server renders for OpenCode.
func openCodeOwnedEntry(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"mcp": map[string]any{testServer().Name: serverEntry(entryOpenCode, testServer())}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func detectionFor(t *testing.T, detections []Detection, id ID) Detection {
	t.Helper()
	for _, detection := range detections {
		if detection.ID == id {
			return detection
		}
	}
	t.Fatalf("%s detection missing", id)
	return Detection{}
}

func TestOpenCodeGlobalDirectoryFollowsXDGOnEveryPlatform(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		home := t.TempDir()
		env := map[string]string{"PATH": "", "APPDATA": `C:\Users\lael\AppData\Roaming`}
		installer, err := New(testServer(), WithEnvironment(DetectOptions{Platform: platform, HomeDir: home, Env: env}))
		if err != nil {
			t.Fatal(err)
		}
		detection := detectionFor(t, installer.Detect(context.Background()), OpenCode)
		want := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		if detection.ConfigError != "" || detection.ConfigPath != want {
			t.Fatalf("%s: OpenCode config = %q (error %q), want %q", platform, detection.ConfigPath, detection.ConfigError, want)
		}
	}
	windows := runtimeEnvironment{platform: "windows", homeDir: `C:\Users\lael`, env: map[string]string{"XDG_CONFIG_HOME": `D:\config`}}
	if resolved := windows.xdgConfig(); resolved.reason != "" || resolved.path != `D:\config` {
		t.Fatalf("Windows XDG_CONFIG_HOME was not accepted: %#v", resolved)
	}
	windows.env["XDG_CONFIG_HOME"] = `config`
	if resolved := windows.xdgConfig(); resolved.reason == "" {
		t.Fatalf("relative Windows XDG_CONFIG_HOME was accepted: %#v", resolved)
	}
}

func TestOpenCodePrefersJSONCAndNeverRefusesBothFiles(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	directory := filepath.Join(home, ".config", "opencode")
	human := filepath.Join(directory, "opencode.jsonc")
	standard := filepath.Join(directory, "opencode.json")
	ctx := context.Background()

	if got := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]; got.State != ChangeReady || got.Path != human {
		t.Fatalf("fresh install should target opencode.jsonc: %#v", got)
	}

	writeFile(t, standard, "{}")
	if got := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]; got.State != ChangeReady || got.Path != standard {
		t.Fatalf("lone opencode.json should be used: %#v", got)
	}

	writeFile(t, human, "{}")
	change := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]
	if change.State != ChangeReady || change.Path != human || change.Reason != "" {
		t.Fatalf("both files present should target opencode.jsonc without complaint: %#v", change)
	}
	detection := detectionFor(t, installer.Detect(ctx), OpenCode)
	if detection.ConfigError != "" || detection.ConfigPath != human {
		t.Fatalf("detection should resolve opencode.jsonc: %#v", detection)
	}
	for _, path := range []string{directory, standard, human} {
		if !containsString(detection.Evidence, path) {
			t.Fatalf("evidence %v lacks %s", detection.Evidence, path)
		}
	}

	project := t.TempDir()
	scope := ProjectScopeDir(project)
	if got := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{Scope: scope}).Changes()[0]; got.Path != filepath.Join(project, "opencode.jsonc") {
		t.Fatalf("fresh project install should target opencode.jsonc: %#v", got)
	}
	writeFile(t, filepath.Join(project, "opencode.json"), "{}")
	if got := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{Scope: scope}).Changes()[0]; got.Path != filepath.Join(project, "opencode.json") {
		t.Fatalf("lone project opencode.json should be used: %#v", got)
	}
	writeFile(t, filepath.Join(project, "opencode.jsonc"), "{}")
	if got := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{Scope: scope}).Changes()[0]; got.Path != filepath.Join(project, "opencode.jsonc") || got.State != ChangeReady {
		t.Fatalf("project opencode.jsonc should win: %#v", got)
	}
}

func TestOpenCodeLowerLayerConflicts(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	directory := filepath.Join(home, ".config", "opencode")
	human := filepath.Join(directory, "opencode.jsonc")
	standard := filepath.Join(directory, "opencode.json")
	ctx := context.Background()
	writeFile(t, human, "{}")
	writeFile(t, standard, openCodeEntry("/other"))

	conflict := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]
	if conflict.State != ChangeConflict || !strings.Contains(conflict.Reason, standard) || !strings.Contains(conflict.Reason, "beneath") {
		t.Fatalf("differing lower layer should conflict: %#v", conflict)
	}
	if result := installer.Apply(ctx, installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}))[0]; result.State != ApplyConflict || result.Reason != conflict.Reason {
		t.Fatalf("apply should report the layer conflict: %#v", result)
	}

	replacePlan := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{ConflictPolicy: ConflictReplace})
	ready := replacePlan.Changes()[0]
	if ready.State != ChangeReady || ready.Action != "add" || ready.Path != human || !strings.Contains(ready.Reason, standard) {
		t.Fatalf("replace policy should proceed with a warning: %#v", ready)
	}
	if result := installer.Apply(ctx, replacePlan)[0]; result.State != Applied || result.Reason != ready.Reason {
		t.Fatalf("applied result should retain the warning: %#v", result)
	}
	if raw, err := os.ReadFile(standard); err != nil || string(raw) != openCodeEntry("/other") {
		t.Fatalf("lower layer must never be written: %v %s", err, raw)
	}

	// An identical entry in a lower layer is harmless when adding.
	writeFile(t, standard, openCodeOwnedEntry(t))
	if got := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]; got.State != ChangeNoop || got.Reason != "already registered" {
		t.Fatalf("identical lower layer should not warn: %#v", got)
	}

	// Any entry in a lower layer matters when removing.
	removal := installer.Plan(ctx, []ID{OpenCode}, Absent, PlanOptions{}).Changes()[0]
	if removal.State != ChangeConflict || !strings.Contains(removal.Reason, "stays registered") {
		t.Fatalf("removal with a lower-layer entry should conflict: %#v", removal)
	}
	removed := installer.Apply(ctx, installer.Plan(ctx, []ID{OpenCode}, Absent, PlanOptions{ConflictPolicy: ConflictReplace}))[0]
	if removed.State != Applied || removed.Action != "remove" || !strings.Contains(removed.Reason, standard) {
		t.Fatalf("replace removal should proceed with a warning: %#v", removed)
	}
	noop := installer.Plan(ctx, []ID{OpenCode}, Absent, PlanOptions{ConflictPolicy: ConflictReplace}).Changes()[0]
	if noop.State != ChangeNoop || !strings.HasPrefix(noop.Reason, "not registered; ") {
		t.Fatalf("noop removal should combine the reason and warning: %#v", noop)
	}
}

func TestOpenCodeLegacyAndEnvironmentLayers(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "custom.jsonc")
	customDir := t.TempDir()
	installer, home := installerWithEnv(t, map[string]string{
		"OPENCODE_CONFIG":         custom,
		"OPENCODE_CONFIG_DIR":     customDir,
		"OPENCODE_CONFIG_CONTENT": openCodeEntry("/inline"),
	})
	directory := filepath.Join(home, ".config", "opencode")
	ctx := context.Background()

	reasons := func() string {
		return installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0].Reason
	}
	if reason := reasons(); !strings.Contains(reason, "$OPENCODE_CONFIG_CONTENT") || !strings.Contains(reason, "overrides") {
		t.Fatalf("inline content should be an overriding layer: %q", reason)
	}

	writeFile(t, custom, openCodeEntry("/custom"))
	if reason := reasons(); !strings.Contains(reason, "$OPENCODE_CONFIG ("+custom+")") {
		t.Fatalf("OPENCODE_CONFIG file should be inspected: %q", reason)
	}
	writeFile(t, filepath.Join(customDir, "opencode.jsonc"), openCodeEntry("/dir"))
	if reason := reasons(); !strings.Contains(reason, "$OPENCODE_CONFIG_DIR") {
		t.Fatalf("OPENCODE_CONFIG_DIR files should be inspected: %q", reason)
	}
	writeFile(t, filepath.Join(home, ".opencode", "opencode.json"), openCodeEntry("/home"))
	if reason := reasons(); !strings.Contains(reason, filepath.Join(home, ".opencode", "opencode.json")) {
		t.Fatalf("~/.opencode files should be inspected: %q", reason)
	}
	legacy := filepath.Join(directory, "config.json")
	writeFile(t, legacy, openCodeEntry("/legacy"))
	if reason := reasons(); !strings.Contains(reason, legacy) || !strings.Contains(reason, "beneath") {
		t.Fatalf("legacy config.json should be a lower layer: %q", reason)
	}

	detection := detectionFor(t, installer.Detect(ctx), OpenCode)
	if !containsString(detection.Evidence, legacy) || !containsString(detection.Evidence, custom) {
		t.Fatalf("evidence should list legacy and custom files: %v", detection.Evidence)
	}
}

func TestOpenCodeUnreadableLayerBlocksPlanning(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	directory := filepath.Join(home, ".config", "opencode")
	writeFile(t, filepath.Join(directory, "opencode.jsonc"), "{}")
	standard := filepath.Join(directory, "opencode.json")
	writeFile(t, standard, `{"mcp":{},"mcp":{}}`)
	change := installer.Plan(context.Background(), []ID{OpenCode}, Present, PlanOptions{ConflictPolicy: ConflictReplace}).Changes()[0]
	if change.State != ChangeUnavailable || !strings.Contains(change.Reason, "inspect "+standard) || !strings.Contains(change.Reason, "duplicate") {
		t.Fatalf("unreadable layer should block planning: %#v", change)
	}
}

func TestOpenCodeProjectLayers(t *testing.T) {
	installer, _ := installerWithEnv(t, nil)
	project := t.TempDir()
	scope := ProjectScopeDir(project)
	local := filepath.Join(project, ".opencode", "opencode.json")
	writeFile(t, local, openCodeEntry("/local"))
	ctx := context.Background()

	change := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{Scope: scope}).Changes()[0]
	if change.State != ChangeConflict || !strings.Contains(change.Reason, local) || !strings.Contains(change.Reason, "overrides") {
		t.Fatalf(".opencode directory should be an overriding layer: %#v", change)
	}

	detections, err := installer.DetectScoped(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	detection := detectionFor(t, detections, OpenCode)
	if detection.State != Detected || !containsString(detection.Evidence, local) || detection.ConfigPath != filepath.Join(project, "opencode.jsonc") {
		t.Fatalf("project detection should count .opencode files as evidence: %#v", detection)
	}

	// Global OpenCode configuration is deliberately not a project-scope layer:
	// differing per-project settings are the point of project scope.
	writeFile(t, filepath.Join(project, ".opencode", "opencode.json"), "{}")
	installerGlobal, home := installerWithEnv(t, nil)
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), openCodeEntry("/global"))
	if got := installerGlobal.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{Scope: scope}).Changes()[0]; got.State != ChangeReady || got.Reason != "" {
		t.Fatalf("global entry must not conflict with project scope: %#v", got)
	}
}

func TestFreshJSONCFilesRenderAsIndentedJSON(t *testing.T) {
	for _, id := range []ID{OpenCode, Zed, VSCode} {
		rendered, err := RenderConfig(id, testServer())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rendered, "\t") || !strings.HasPrefix(rendered, "{\n  \"") || !strings.HasSuffix(rendered, "}\n") {
			t.Fatalf("%s fresh render is not conventionally indented:\n%s", id, rendered)
		}
	}
	installer, home := installerWithEnv(t, nil)
	plan := installer.Plan(context.Background(), []ID{OpenCode}, Present, PlanOptions{})
	if after := plan.Changes()[0].After; strings.Contains(after, "\t") {
		t.Fatalf("fresh OpenCode plan output uses tabs:\n%s", after)
	}
	human := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	writeFile(t, human, "{\n\t// keep\n\t\"theme\": \"dark\",\n}\n")
	after := installer.Plan(context.Background(), []ID{OpenCode}, Present, PlanOptions{}).Changes()[0].After
	if !strings.Contains(after, "// keep") || !strings.Contains(after, "\"theme\"") {
		t.Fatalf("existing JSONC lost content:\n%s", after)
	}
}

func TestNoopChangesExplainThemselves(t *testing.T) {
	installer, _ := installerWithEnv(t, nil)
	ctx := context.Background()
	if got := installer.Plan(ctx, []ID{Cursor}, Absent, PlanOptions{}).Changes()[0]; got.State != ChangeNoop || got.Reason != "not registered" {
		t.Fatalf("absent noop should explain itself: %#v", got)
	}
	installer.Ensure(ctx, []ID{Cursor}, Present, PlanOptions{})
	result := installer.Ensure(ctx, []ID{Cursor}, Present, PlanOptions{})[0]
	if result.State != ApplyNoop || result.Reason != "already registered" {
		t.Fatalf("present noop should explain itself in results too: %#v", result)
	}
}

func TestDetectScopedUsesInstallerEnvironment(t *testing.T) {
	installer, _ := installerWithEnv(t, nil)
	project := t.TempDir()
	writeFile(t, filepath.Join(project, ".cursor", "mcp.json"), "{}")
	detections, err := installer.DetectScoped(context.Background(), ProjectScopeDir(project))
	if err != nil {
		t.Fatal(err)
	}
	cursor := detectionFor(t, detections, Cursor)
	if cursor.State != Detected || cursor.Scope != ScopeProject || cursor.ScopeDir != project {
		t.Fatalf("unexpected project detection: %#v", cursor)
	}
	global, err := installer.DetectScoped(context.Background(), Scope{})
	if err != nil || detectionFor(t, global, Cursor).Scope != ScopeGlobal {
		t.Fatalf("zero scope should behave like Detect: %v", err)
	}
	if _, err := installer.DetectScoped(context.Background(), Scope{Mode: ScopeProject}); err == nil {
		t.Fatal("invalid scope should be rejected")
	}
	if _, err := installer.DetectScoped(context.Background(), Scope{Mode: "workspace", Dir: project}); err == nil {
		t.Fatal("unknown scope mode should be rejected")
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
