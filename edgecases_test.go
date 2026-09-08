package detectharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgumentlessServerIsIdempotentInEveryFormat(t *testing.T) {
	home := t.TempDir()
	installer, err := New(StdioServer{Name: "bare", Command: "/bin/bare"}, WithEnvironment(DetectOptions{
		Platform: "linux",
		HomeDir:  home,
		Env:      map[string]string{"PATH": "", "XDG_CONFIG_HOME": filepath.Join(home, ".config")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ids := []ID{Cursor, VSCode, Zed, Codex, Continue, OpenCode}
	for _, result := range installer.Ensure(ctx, ids, Present, PlanOptions{}) {
		if result.State != Applied {
			t.Fatalf("add failed: %#v", result)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "null") || !strings.Contains(string(raw), `"args": []`) {
		t.Fatalf("argument-less server must encode args as an empty array:\n%s", raw)
	}
	for _, change := range installer.Plan(ctx, ids, Present, PlanOptions{}).Changes() {
		if change.State != ChangeNoop {
			t.Fatalf("re-plan of an argument-less server was not a noop: %#v", change)
		}
	}
}

func TestNullArgsAndEmptyContainersCountAsOwned(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	config := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, config, `{"mcpServers":{"example.mcp":{"command":"/opt/Example MCP/server","args":["mcp","--stdio"],"env":{"MODE":"write"},"disabled":null}}}`)
	if change := installer.Plan(context.Background(), []ID{Cursor}, Present, PlanOptions{}).Changes()[0]; change.State != ChangeNoop {
		t.Fatalf("null and empty members must not make an entry foreign: %#v", change)
	}
	writeFile(t, config, `{"mcpServers":{"example.mcp":{"command":"/opt/Example MCP/server","args":["mcp","--stdio"],"env":{"MODE":"write","EXTRA":"x"}}}}`)
	if change := installer.Plan(context.Background(), []ID{Cursor}, Present, PlanOptions{}).Changes()[0]; change.State != ChangeConflict {
		t.Fatalf("a real difference must still be foreign: %#v", change)
	}
}

func TestNullContainersAreTreatedAsEmpty(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": null}`)
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), "{\n  // c\n  \"mcp\": null\n}\n")
	writeFile(t, filepath.Join(home, ".continue", "config.yaml"), "name: Local Config\nmcpServers:\n")
	for _, result := range installer.Ensure(ctx, []ID{Cursor, OpenCode, Continue}, Present, PlanOptions{}) {
		if result.State != Applied {
			t.Fatalf("null container should be treated as empty: %#v", result)
		}
	}
	for _, change := range installer.Plan(ctx, []ID{Cursor, OpenCode, Continue}, Present, PlanOptions{}).Changes() {
		if change.State != ChangeNoop {
			t.Fatalf("expected noop after add: %#v", change)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "// c") || !strings.Contains(string(raw), "\"mcp\": {\n    \"example.mcp\": {") {
		t.Fatalf("null JSONC container was not replaced in place:\n%s", raw)
	}
}

func TestJSONCEmptyContainerKeepsItsComment(t *testing.T) {
	after := jsoncFixture(t, "{\n  \"mcp\": { // todo\n  }\n}\n", Present)
	if !strings.Contains(after, "// todo") {
		t.Fatalf("comment inside an empty container was lost:\n%s", after)
	}
}

func TestZedGlobalConfigPathPerPlatform(t *testing.T) {
	home := t.TempDir()
	for platform, want := range map[string]string{
		"darwin": filepath.Join(home, ".config", "zed", "settings.json"),
		"linux":  filepath.Join(home, "xdg", "zed", "settings.json"),
	} {
		installer, err := New(testServer(), WithEnvironment(DetectOptions{Platform: platform, HomeDir: home, Env: map[string]string{"PATH": "", "XDG_CONFIG_HOME": filepath.Join(home, "xdg")}}))
		if err != nil {
			t.Fatal(err)
		}
		if got := detectionFor(t, installer.Detect(context.Background()), Zed).ConfigPath; got != want {
			t.Fatalf("%s: Zed config = %q, want %q", platform, got, want)
		}
	}
}

func TestEmptyXDGConfigHomeFallsBackToHome(t *testing.T) {
	installer, home := installerWithEnv(t, map[string]string{"XDG_CONFIG_HOME": ""})
	zed := detectionFor(t, installer.Detect(context.Background()), Zed)
	if zed.ConfigError != "" || zed.ConfigPath != filepath.Join(home, ".config", "zed", "settings.json") {
		t.Fatalf("empty XDG_CONFIG_HOME should fall back to ~/.config: %#v", zed)
	}
}

func TestFilePathEntryDoesNotMakeCommandDetectionUnavailable(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "notadir")
	writeFile(t, file, "x")
	installer, err := New(testServer(), WithEnvironment(DetectOptions{Platform: "linux", HomeDir: home, Env: map[string]string{"PATH": file}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := detectionFor(t, installer.Detect(context.Background()), ClaudeCode); got.State != NotDetected {
		t.Fatalf("a file on PATH should not make detection unavailable: %#v", got)
	}
}

func TestMissingProjectDirectoryIsRejected(t *testing.T) {
	installer, _ := installerWithEnv(t, nil)
	missing := filepath.Join(t.TempDir(), "typo")
	ctx := context.Background()
	for _, change := range installer.Plan(ctx, []ID{Cursor, "bogus"}, Present, PlanOptions{Scope: ProjectScopeDir(missing)}).Changes() {
		if change.State != ChangeUnavailable || !strings.Contains(change.Reason, "does not exist") || change.Scope != ScopeProject || change.ScopeDir != missing {
			t.Fatalf("missing project directory should be unavailable with scope fields: %#v", change)
		}
		if change.HarnessID == Cursor && change.Name != "Cursor" {
			t.Fatalf("invalid-selection changes should carry the harness name: %#v", change)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("planning must not create the project directory")
	}
	detections, err := installer.DetectScoped(ctx, ProjectScopeDir(missing))
	if err != nil {
		t.Fatal(err)
	}
	if cursor := detectionFor(t, detections, Cursor); cursor.State != Unavailable || !strings.Contains(cursor.Reason, "does not exist") {
		t.Fatalf("detection in a missing project directory should be unavailable: %#v", cursor)
	}
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "x")
	if change := installer.Plan(ctx, []ID{Cursor}, Present, PlanOptions{Scope: ProjectScopeDir(file)}).Changes()[0]; change.State != ChangeUnavailable || !strings.Contains(change.Reason, "not a directory") {
		t.Fatalf("a file as project directory should be unavailable: %#v", change)
	}
}

func TestUnknownHarnessInProjectPlanCarriesScope(t *testing.T) {
	installer, _ := installerWithEnv(t, nil)
	project := t.TempDir()
	change := installer.Plan(context.Background(), []ID{"bogus"}, Present, PlanOptions{Scope: ProjectScopeDir(project)}).Changes()[0]
	if change.State != ChangeUnavailable || change.Scope != ScopeProject || change.ScopeDir != project {
		t.Fatalf("unknown harness change lacks scope fields: %#v", change)
	}
}

func TestScopeDirectoryIsTrimmed(t *testing.T) {
	project := t.TempDir()
	scope, err := Scope{Mode: ScopeProject, Dir: "  " + project + "  "}.normalize()
	if err != nil || scope.Dir != project {
		t.Fatalf("scope dir was not trimmed: %#v %v", scope, err)
	}
}

func TestOpenCodeLayersAreDeduplicatedAndSkipTheTarget(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	ctx := context.Background()

	// OPENCODE_CONFIG_DIR pointing at ~/.opencode lists each file once.
	installer.runtime.env["OPENCODE_CONFIG_DIR"] = filepath.Join(home, ".opencode")
	local := filepath.Join(home, ".opencode", "opencode.json")
	writeFile(t, local, openCodeEntry("/dup"))
	change := installer.Plan(ctx, []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]
	if change.State != ChangeConflict || strings.Count(change.Reason, local) != 1 {
		t.Fatalf("duplicate layers should be reported once: %#v", change)
	}

	// OPENCODE_CONFIG aliasing the managed file through an uncleaned path is
	// not a separate layer, so removal is a plain ready change.
	delete(installer.runtime.env, "OPENCODE_CONFIG_DIR")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	installer.runtime.env["OPENCODE_CONFIG"] = filepath.Join(filepath.Dir(target), ".", "opencode.jsonc")
	writeFile(t, target, openCodeOwnedEntry(t))
	if got := installer.Plan(ctx, []ID{OpenCode}, Absent, PlanOptions{}).Changes()[0]; got.State != ChangeReady || got.Reason != "" {
		t.Fatalf("a layer aliasing the target must be ignored: %#v", got)
	}
}

func TestSymlinkedLayerIsFollowed(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	real := filepath.Join(home, "real.json")
	writeFile(t, real, openCodeEntry("/linked"))
	link := filepath.Join(home, ".config", "opencode", "config.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	change := installer.Plan(context.Background(), []ID{OpenCode}, Present, PlanOptions{}).Changes()[0]
	if change.State != ChangeConflict || !strings.Contains(change.Reason, link) {
		t.Fatalf("symlinked layer should be inspected, not rejected: %#v", change)
	}
}

func TestProjectScopeAtFilesystemRootDoesNotPanic(t *testing.T) {
	installer, _ := installerWithEnv(t, nil)
	detections, err := installer.DetectScoped(context.Background(), ProjectScopeDir(string(filepath.Separator)))
	if err != nil {
		t.Fatal(err)
	}
	if got := detectionFor(t, detections, OpenCode); got.ConfigPath != filepath.Join(string(filepath.Separator), "opencode.jsonc") {
		t.Fatalf("unexpected root project resolution: %#v", got)
	}
}
