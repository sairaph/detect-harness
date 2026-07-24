# detect-harness

`detect-harness` detects AI coding harnesses and manages one canonical stdio
MCP server definition across every supported client configuration.

The repository contains:

- An importable Go library.
- A versioned JSON companion binary.
- Typed Node, Python, and Rust wrappers around the binary.
- JSON schemas for cross-language protocol conformance.

## Supported harnesses

Claude Desktop, Claude Code, Cursor, Codex CLI, Gemini CLI, Windsurf, Zed,
Cline, Roo Code, Amazon Q Developer CLI, Continue, OpenCode, and VS Code.

## Go library

```go
package main

import (
    "context"
    "log"

    detectharness "github.com/sairaph/detect-harness"
)

func configure(command string) {
    server := detectharness.StdioServer{
        Name:    "my-mcp",
        Command: command,
        Args:    []string{"mcp"},
        Env:     map[string]string{"MY_MCP_MODE": "write"},
    }

    installer, err := detectharness.New(server)
    if err != nil {
        log.Fatal(err)
    }

    ctx := context.Background()
    detected := installer.Detect(ctx)
    _ = detected // Present detections in your own CLI or TUI.

    plan := installer.Plan(ctx, []detectharness.ID{
        detectharness.Cursor,
        detectharness.Codex,
    }, detectharness.Present, detectharness.PlanOptions{})

    // Display plan.Changes() and request confirmation before applying.
    results := installer.Apply(ctx, plan)
    _ = results
}
```

`Installer.Ensure` is a plan-and-apply convenience method for unattended
flows. `RenderConfig` generates a standalone configuration without filesystem
access. `DetectHarnesses` performs detection without requiring a server.

## Conflict behavior

The default `ConflictError` policy does not overwrite or remove a same-name
entry unless it exactly matches the canonical server definition. Use
`ConflictReplace` only when the calling installer owns that stable server name
and intentionally wants to update or remove it.

## Companion binary

Build locally:

```sh
go build -o detect-harness ./cmd/detect-harness
```

The binary accepts one JSON document on standard input and emits one JSON
document on standard output. Configuration values are never passed in process
arguments.

```sh
printf '%s' '{"version":1,"operation":"detect"}' | ./detect-harness
```

See [`protocol/`](protocol/) for schemas and operation details.

## Language wrappers

- Node: [`wrappers/node`](wrappers/node)
- Python: [`wrappers/python`](wrappers/python)
- Rust: [`wrappers/rust`](wrappers/rust)

Wrappers resolve an explicitly configured binary first, then
`DETECT_HARNESS_BIN`, then `detect-harness` on `PATH`. They do not currently
download or execute code during package installation.

## Safety model

- Detection distinguishes present, absent, and unavailable states.
- Configuration parsing rejects invalid roots and wrong container types.
- Same-name foreign entries are conflicts unless replacement is explicit.
- Plans retain snapshots and recheck files immediately before atomic
  publication, reducing the chance of overwriting concurrent external edits.
- Writes use same-directory staging, restrictive permissions, flushes, and
  atomic replacement.
- JSONC comments, TOML source outside managed tables, YAML nodes, and unrelated
  configuration values are retained.
- Configuration files are bounded to 8 MiB and symbolic-link targets are
  rejected.
- Multi-harness application reports partial results and does not hide failures.

## Scope

Version 1 installs local stdio MCP servers. Remote HTTP and SSE transports need
client-specific capability handling and are intentionally not implied by the
current API.

## License

MIT, copyright Łael Al-Halawani.
