package detectharness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"
)

func TestHuJSONStandardizeDoesNotMutateSeparateInput(t *testing.T) {
	original := []byte("{\n// comment\n\"value\": true,\n}\n")
	preserved := bytes.Clone(original)
	if _, err := hujson.Standardize(bytes.Clone(preserved)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(preserved, []byte("// comment")) {
		t.Fatalf("standardize mutated a separate input: %q", preserved)
	}
}

func TestHumanJSONMutationPreservesUnrelatedComment(t *testing.T) {
	raw := []byte("{\n  // comment\n  \"theme\": \"dark\",\n}\n")
	document, err := hujson.Parse(bytes.Clone(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := mutateHumanJSON(&document, "mcp", "example.mcp", serverEntry(entryOpenCode, testServer()), Present); err != nil {
		t.Fatal(err)
	}
	document.Format()
	if output := document.Pack(); !bytes.Contains(output, []byte("// comment")) {
		t.Fatalf("mutation lost comment:\n%s", output)
	}
}

func testServer() StdioServer {
	return StdioServer{
		Name:    "example.mcp",
		Command: "/opt/Example MCP/server",
		Args:    []string{"mcp", "--stdio"},
		Env:     map[string]string{"MODE": "write"},
	}
}

func testInstaller(t *testing.T) (*Installer, string) {
	t.Helper()
	home := t.TempDir()
	installer, err := New(testServer(), WithEnvironment(DetectOptions{
		Platform: "linux",
		HomeDir:  home,
		Env: map[string]string{
			"PATH":            "",
			"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return installer, home
}

func TestSupportedCatalog(t *testing.T) {
	harnesses := Supported()
	if len(harnesses) != 13 {
		t.Fatalf("got %d harnesses, want 13", len(harnesses))
	}
	seen := make(map[ID]bool, len(harnesses))
	for _, harness := range harnesses {
		if seen[harness.ID] || harness.Name == "" || harness.ReloadHint == "" {
			t.Fatalf("invalid harness metadata: %#v", harness)
		}
		seen[harness.ID] = true
	}
}

func TestRenderConfigShapes(t *testing.T) {
	server := testServer()

	standardRaw, err := RenderConfig(Cursor, server)
	if err != nil {
		t.Fatal(err)
	}
	var standard map[string]any
	if err := json.Unmarshal([]byte(standardRaw), &standard); err != nil {
		t.Fatal(err)
	}
	standardEntry := standard["mcpServers"].(map[string]any)[server.Name].(map[string]any)
	if standardEntry["command"] != server.Command || standardEntry["env"].(map[string]any)["MODE"] != "write" {
		t.Fatalf("unexpected standard entry: %#v", standardEntry)
	}

	openCodeRaw, err := RenderConfig(OpenCode, server)
	if err != nil {
		t.Fatal(err)
	}
	var openCode map[string]any
	if err := json.Unmarshal([]byte(openCodeRaw), &openCode); err != nil {
		t.Fatal(err)
	}
	openCodeEntry := openCode["mcp"].(map[string]any)[server.Name].(map[string]any)
	if openCodeEntry["type"] != "local" || len(openCodeEntry["command"].([]any)) != 3 {
		t.Fatalf("unexpected OpenCode entry: %#v", openCodeEntry)
	}

	vsCodeRaw, err := RenderConfig(VSCode, server)
	if err != nil {
		t.Fatal(err)
	}
	var vsCode map[string]any
	if err := json.Unmarshal([]byte(vsCodeRaw), &vsCode); err != nil {
		t.Fatal(err)
	}
	if got := vsCode["servers"].(map[string]any)[server.Name].(map[string]any)["type"]; got != "stdio" {
		t.Fatalf("VS Code type = %v", got)
	}

	tomlRaw, err := RenderConfig(Codex, server)
	if err != nil {
		t.Fatal(err)
	}
	var tomlConfig map[string]any
	if err := toml.Unmarshal([]byte(tomlRaw), &tomlConfig); err != nil {
		t.Fatalf("invalid rendered TOML: %v\n%s", err, tomlRaw)
	}
	if !semanticEqual(tomlConfig["mcp_servers"].(map[string]any)[server.Name], standardEntryForTest(server)) {
		t.Fatalf("unexpected TOML entry: %#v", tomlConfig)
	}

	yamlRaw, err := RenderConfig(Continue, server)
	if err != nil {
		t.Fatal(err)
	}
	var yamlConfig map[string]any
	if err := yaml.Unmarshal([]byte(yamlRaw), &yamlConfig); err != nil {
		t.Fatalf("invalid rendered YAML: %v\n%s", err, yamlRaw)
	}
	entries := yamlConfig["mcpServers"].([]any)
	if entries[0].(map[string]any)["name"] != server.Name {
		t.Fatalf("unexpected YAML entry: %#v", entries[0])
	}
	if yamlConfig["name"] != "Local Config" || yamlConfig["version"] != "0.0.1" || yamlConfig["schema"] != "v1" {
		t.Fatalf("rendered Continue config lacks required metadata: %#v", yamlConfig)
	}
}

func TestJSONPreservesLargeNumbersAndRejectsDuplicates(t *testing.T) {
	installer, home := testInstaller(t)
	config := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	const largeNumber = "9007199254740993"
	if err := os.WriteFile(config, []byte(`{"unrelated":`+largeNumber+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installer.Ensure(context.Background(), []ID{Cursor}, Present, PlanOptions{})[0].State; got != Applied {
		t.Fatalf("JSON update state = %s", got)
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(largeNumber)) {
		t.Fatalf("large number changed:\n%s", raw)
	}
	if err := os.WriteFile(config, []byte(`{"mcpServers":{},"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	change := installer.Plan(context.Background(), []ID{Cursor}, Present, PlanOptions{}).Changes()[0]
	if change.State != ChangeUnavailable || !strings.Contains(change.Reason, "duplicate") {
		t.Fatalf("duplicate keys were not rejected: %#v", change)
	}
}

func TestContinueRejectsMultipleYAMLDocuments(t *testing.T) {
	installer, home := testInstaller(t)
	config := filepath.Join(home, ".continue", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("name: first\n---\nname: second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	change := installer.Plan(context.Background(), []ID{Continue}, Present, PlanOptions{}).Changes()[0]
	if change.State != ChangeUnavailable || !strings.Contains(change.Reason, "multiple YAML") {
		t.Fatalf("multiple YAML documents were not rejected: %#v", change)
	}
}

func TestNewClonesServerReferences(t *testing.T) {
	server := testServer()
	installer, err := New(server)
	if err != nil {
		t.Fatal(err)
	}
	server.Args[0] = "changed"
	server.Env["MODE"] = "changed"
	if installer.server.Args[0] != "mcp" || installer.server.Env["MODE"] != "write" {
		t.Fatalf("installer retained caller-owned references: %#v", installer.server)
	}
}

func TestSerializedPlanDoesNotExposeConfigurationContents(t *testing.T) {
	installer, _ := testInstaller(t)
	plan := installer.Plan(context.Background(), []ID{Cursor}, Present, PlanOptions{})
	raw, err := json.Marshal(plan.Changes())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("MODE")) || bytes.Contains(raw, []byte("/opt/Example MCP")) {
		t.Fatalf("serialized plan exposed configuration content: %s", raw)
	}
}

func TestWindowsEnvironmentLookupIsCaseInsensitive(t *testing.T) {
	environment := runtimeEnvironment{platform: "windows", env: map[string]string{"Path": `C:\\bin`, "AppData": `C:\\Users\\lael\\AppData\\Roaming`}}
	if value, found := environment.lookupEnv("PATH"); !found || value != `C:\\bin` {
		t.Fatalf("PATH lookup = %q, %v", value, found)
	}
	if resolved := environment.appData(); resolved.reason != "" || resolved.path == "" {
		t.Fatalf("AppData lookup failed: %#v", resolved)
	}
}

func TestStaleConfigLockIsRecovered(t *testing.T) {
	installer, home := testInstaller(t)
	config := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(filepath.Dir(config), ".mcp.json.detect-harness.lock")
	if err := os.WriteFile(lock, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	if got := installer.Ensure(context.Background(), []ID{Cursor}, Present, PlanOptions{})[0].State; got != Applied {
		t.Fatalf("stale lock was not recovered: %s", got)
	}
}

func standardEntryForTest(server StdioServer) map[string]any {
	return map[string]any{
		"command": server.Command,
		"args":    server.Args,
		"env":     server.Env,
	}
}

func TestPlanApplyRoundTripAllFormats(t *testing.T) {
	installer, home := testInstaller(t)
	ids := []ID{Cursor, Codex, Continue, OpenCode, VSCode}
	ctx := context.Background()

	plan := installer.Plan(ctx, ids, Present, PlanOptions{})
	for _, change := range plan.Changes() {
		if change.State != ChangeReady || change.Action != "add" {
			t.Fatalf("unexpected add plan: %#v", change)
		}
	}
	for _, result := range installer.Apply(ctx, plan) {
		if result.State != Applied {
			t.Fatalf("add failed: %#v", result)
		}
	}

	for _, change := range installer.Plan(ctx, ids, Present, PlanOptions{}).Changes() {
		if change.State != ChangeNoop {
			t.Fatalf("second plan was not idempotent: %#v", change)
		}
	}

	removePlan := installer.Plan(ctx, ids, Absent, PlanOptions{})
	for _, result := range installer.Apply(ctx, removePlan) {
		if result.State != Applied {
			t.Fatalf("remove failed: %#v", result)
		}
	}
	for _, change := range installer.Plan(ctx, ids, Absent, PlanOptions{}).Changes() {
		if change.State != ChangeNoop {
			t.Fatalf("second removal was not idempotent: %#v", change)
		}
	}

	if _, err := os.Stat(filepath.Join(home, ".cursor", "mcp.json")); err != nil {
		t.Fatalf("expected managed config to remain with unrelated root: %v", err)
	}
}

func TestConflictPolicyAndConcurrentChange(t *testing.T) {
	installer, home := testInstaller(t)
	config := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := `{"mcpServers":{"example.mcp":{"command":"other"}},"keep":true}`
	if err := os.WriteFile(config, []byte(foreign), 0o640); err != nil {
		t.Fatal(err)
	}

	conflict := installer.Plan(context.Background(), []ID{Cursor}, Present, PlanOptions{}).Changes()[0]
	if conflict.State != ChangeConflict {
		t.Fatalf("expected conflict, got %#v", conflict)
	}
	replacePlan := installer.Plan(context.Background(), []ID{Cursor}, Present, PlanOptions{ConflictPolicy: ConflictReplace})
	if got := replacePlan.Changes()[0]; got.State != ChangeReady || got.Action != "update" {
		t.Fatalf("unexpected replacement plan: %#v", got)
	}
	if err := os.WriteFile(config, []byte(`{"changed":true}`), 0o640); err != nil {
		t.Fatal(err)
	}
	result := installer.Apply(context.Background(), replacePlan)[0]
	if result.State != ApplyFailed || !strings.Contains(result.Reason, "changed after") {
		t.Fatalf("expected concurrent change failure, got %#v", result)
	}
}

func TestJSONCInputAndTOMLCommentsRemainValid(t *testing.T) {
	installer, home := testInstaller(t)
	openCodePath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(openCodePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodePath, []byte("{\n  // user setting\n  \"theme\": \"dark\",\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jsoncPlan := installer.Plan(context.Background(), []ID{OpenCode}, Present, PlanOptions{})
	change := jsoncPlan.Changes()[0]
	if !strings.Contains(change.Before, "// user setting") || !strings.Contains(change.After, "// user setting") {
		t.Fatalf("JSONC plan lost comment: %#v", change)
	}
	if !bytes.Contains(jsoncPlan.changes[0].after, []byte("// user setting")) {
		t.Fatalf("internal JSONC plan lost comment:\n%s", jsoncPlan.changes[0].after)
	}
	result := installer.Apply(context.Background(), jsoncPlan)[0]
	if result.State != Applied {
		t.Fatalf("JSONC update failed: %#v", result)
	}
	if !bytes.Contains(jsoncPlan.changes[0].after, []byte("// user setting")) {
		t.Fatalf("apply mutated JSONC plan:\n%s", jsoncPlan.changes[0].after)
	}
	raw, err := os.ReadFile(openCodePath)
	if err != nil {
		t.Fatal(err)
	}
	standardized, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(standardized, &parsed); err != nil || parsed["theme"] != "dark" || !strings.Contains(string(raw), "// user setting") {
		t.Fatalf("updated JSONC is invalid or lost settings/comments: %v\n%s", err, raw)
	}

	tomlPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(tomlPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const originalTOML = "# user comment\nmodel = \"gpt-5\"\n"
	if err := os.WriteFile(tomlPath, []byte(originalTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installer.Ensure(context.Background(), []ID{Codex}, Present, PlanOptions{})[0].State; got != Applied {
		t.Fatalf("TOML add state = %s", got)
	}
	if got := installer.Ensure(context.Background(), []ID{Codex}, Absent, PlanOptions{})[0].State; got != Applied {
		t.Fatalf("TOML remove state = %s", got)
	}
	raw, err = os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# user comment") || !strings.Contains(string(raw), `model = "gpt-5"`) {
		t.Fatalf("TOML siblings were not preserved:\n%s", raw)
	}
}

func TestDetectionReturnsEvidenceAndResolvedPaths(t *testing.T) {
	installer, home := testInstaller(t)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	detections := installer.Detect(context.Background())
	for _, detection := range detections {
		if detection.ID != Cursor {
			continue
		}
		if detection.State != Detected || len(detection.Evidence) == 0 || detection.ConfigPath != filepath.Join(home, ".cursor", "mcp.json") {
			t.Fatalf("unexpected Cursor detection: %#v", detection)
		}
		return
	}
	t.Fatal("Cursor detection missing")
}
