package detectharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsoncFixture plans an OpenCode change against an existing opencode.jsonc
// with the given content and returns the rendered output.
func jsoncFixture(t *testing.T, content string, desired DesiredState) string {
	t.Helper()
	installer, home := installerWithEnv(t, nil)
	human := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	writeFile(t, human, content)
	change := installer.Plan(context.Background(), []ID{OpenCode}, desired, PlanOptions{ConflictPolicy: ConflictReplace}).Changes()[0]
	if change.State != ChangeReady {
		t.Fatalf("expected a ready change, got %#v", change)
	}
	return change.After
}

func TestJSONCEditPreservesTwoSpaceIndentAndStyle(t *testing.T) {
	const before = "{\n  // keep\n  \"theme\": \"dark\",\n  \"mcp\": {\n    \"other\": {\n      \"type\": \"local\",\n      \"command\": [\"x\"]\n    }\n  }\n}\n"
	after := jsoncFixture(t, before, Present)
	const want = "{\n  // keep\n  \"theme\": \"dark\",\n  \"mcp\": {\n    \"other\": {\n      \"type\": \"local\",\n      \"command\": [\"x\"]\n    },\n    \"example.mcp\": {\n      \"command\": [\n        \"/opt/Example MCP/server\",\n        \"mcp\",\n        \"--stdio\"\n      ],\n      \"environment\": {\n        \"MODE\": \"write\"\n      },\n      \"type\": \"local\"\n    }\n  }\n}\n"
	if after != want {
		t.Fatalf("edit changed unrelated formatting:\n--- got\n%s--- want\n%s", after, want)
	}
}

func TestJSONCEditFollowsTabsAndTrailingCommas(t *testing.T) {
	const before = "{\n\t\"theme\": \"dark\",\n\t\"mcp\": {\n\t\t\"other\": {\n\t\t\t\"type\": \"local\",\n\t\t},\n\t},\n}\n"
	after := jsoncFixture(t, before, Present)
	const want = "{\n\t\"theme\": \"dark\",\n\t\"mcp\": {\n\t\t\"other\": {\n\t\t\t\"type\": \"local\",\n\t\t},\n\t\t\"example.mcp\": {\n\t\t\t\"command\": [\n\t\t\t\t\"/opt/Example MCP/server\",\n\t\t\t\t\"mcp\",\n\t\t\t\t\"--stdio\"\n\t\t\t],\n\t\t\t\"environment\": {\n\t\t\t\t\"MODE\": \"write\"\n\t\t\t},\n\t\t\t\"type\": \"local\"\n\t\t},\n\t},\n}\n"
	if after != want {
		t.Fatalf("edit did not follow tab and trailing-comma style:\n--- got\n%s--- want\n%s", after, want)
	}
}

func TestJSONCEditCreatesContainerWithDocumentIndent(t *testing.T) {
	const before = "{\n    \"theme\": \"dark\"\n}\n"
	after := jsoncFixture(t, before, Present)
	const want = "{\n    \"theme\": \"dark\",\n    \"mcp\": {\n        \"example.mcp\": {\n            \"command\": [\n                \"/opt/Example MCP/server\",\n                \"mcp\",\n                \"--stdio\"\n            ],\n            \"environment\": {\n                \"MODE\": \"write\"\n            },\n            \"type\": \"local\"\n        }\n    }\n}\n"
	if after != want {
		t.Fatalf("new container did not follow four-space indent:\n--- got\n%s--- want\n%s", after, want)
	}
	// An empty container gains members on their own lines and a closing brace
	// back at the parent indent.
	if got := jsoncFixture(t, "{\n  \"mcp\": {}\n}\n", Present); !strings.Contains(got, "  \"mcp\": {\n    \"example.mcp\": {\n") || !strings.HasSuffix(got, "    }\n  }\n}\n") {
		t.Fatalf("empty container was not expanded correctly:\n%s", got)
	}
}

func TestJSONCEditPreservesCRLF(t *testing.T) {
	after := jsoncFixture(t, "{\r\n  \"theme\": \"dark\"\r\n}\r\n", Present)
	if strings.Contains(strings.ReplaceAll(after, "\r\n", ""), "\n") || !strings.Contains(after, "\r\n    \"example.mcp\": {\r\n") {
		t.Fatalf("CRLF line endings were not preserved:\n%q", after)
	}
}

func TestJSONCRemovalPreservesStyle(t *testing.T) {
	installer, home := installerWithEnv(t, nil)
	human := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	ctx := context.Background()
	const before = "{\n  // keep\n  \"theme\": \"dark\",\n  \"mcp\": {\n    \"other\": {\n      \"type\": \"local\"\n    }\n  }\n}\n"
	writeFile(t, human, before)
	if result := installer.Ensure(ctx, []ID{OpenCode}, Present, PlanOptions{})[0]; result.State != Applied {
		t.Fatalf("add failed: %#v", result)
	}
	if result := installer.Ensure(ctx, []ID{OpenCode}, Absent, PlanOptions{})[0]; result.State != Applied {
		t.Fatalf("remove failed: %#v", result)
	}
	raw, err := os.ReadFile(human)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != before {
		t.Fatalf("add then remove was not a byte-for-byte round trip:\n--- got\n%s--- want\n%s", raw, before)
	}

	// Removing the only server drops the container without leaving a trailing
	// comma behind the previous member.
	writeFile(t, human, "{\n  \"theme\": \"dark\",\n  \"mcp\": {\n    \"example.mcp\": {\n      \"type\": \"local\",\n      \"command\": [\"/opt/Example MCP/server\", \"mcp\", \"--stdio\"],\n      \"environment\": {\"MODE\": \"write\"}\n    }\n  }\n}\n")
	if result := installer.Ensure(ctx, []ID{OpenCode}, Absent, PlanOptions{})[0]; result.State != Applied {
		t.Fatalf("remove failed: %#v", result)
	}
	raw, err = os.ReadFile(human)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{\n  \"theme\": \"dark\"\n}\n" {
		t.Fatalf("container removal left stray formatting:\n%s", raw)
	}
}

func TestJSONCUpdateReplacesEntryInPlace(t *testing.T) {
	const before = "{\n  \"mcp\": {\n    \"example.mcp\": {\n      \"type\": \"local\",\n      \"command\": [\"old\"]\n    },\n    \"other\": {}\n  }\n}\n"
	after := jsoncFixture(t, before, Present)
	const want = "{\n  \"mcp\": {\n    \"example.mcp\": {\n      \"command\": [\n        \"/opt/Example MCP/server\",\n        \"mcp\",\n        \"--stdio\"\n      ],\n      \"environment\": {\n        \"MODE\": \"write\"\n      },\n      \"type\": \"local\"\n    },\n    \"other\": {}\n  }\n}\n"
	if after != want {
		t.Fatalf("in-place update disturbed siblings:\n--- got\n%s--- want\n%s", after, want)
	}
}
