# detect-harness

[![release](https://img.shields.io/github/v/release/sairaph/detect-harness?label=release)](https://github.com/sairaph/detect-harness/releases)
[![CI](https://github.com/sairaph/detect-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/sairaph/detect-harness/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sairaph/detect-harness.svg)](https://pkg.go.dev/github.com/sairaph/detect-harness)
[![license](https://img.shields.io/github/license/sairaph/detect-harness)](#license)
[![platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)](#supported-harnesses)

Add a polished MCP installer to any Go server. Define one stdio server once,
detect the AI harnesses on the machine, and let `detect-harness` generate and
safely update every client-specific configuration.

Install the Go library:

```bash
go get github.com/sairaph/detect-harness@latest
```

Then detect and configure selected harnesses:

```go
installer, err := detectharness.New(detectharness.StdioServer{
    Name:    "my-mcp",
    Command: "/absolute/path/to/my-mcp",
    Args:    []string{"mcp"},
    Env:     map[string]string{"MY_MCP_MODE": "write"},
})
if err != nil {
    log.Fatal(err)
}

ctx := context.Background()
detected := installer.Detect(ctx)

plan := installer.Plan(ctx, []detectharness.ID{
    detectharness.Cursor,
    detectharness.Codex,
}, detectharness.Present, detectharness.PlanOptions{})

// Show plan.Changes() in your CLI, ask for confirmation, then apply it.
results := installer.Apply(ctx, plan)
```

The library does not provide a TUI or impose an installer flow. Your CLI owns
selection and presentation; `detect-harness` owns detection, valid config
generation, conflict handling, and safe persistence.

Need the language-neutral companion for a Node, Python, Rust, or other server?

```bash
go install github.com/sairaph/detect-harness/cmd/detect-harness@latest
```

Set `DETECT_HARNESS_BIN` to that executable, or pass its path directly to a
wrapper. Tagged releases also include native binaries and packaged wrappers for
all supported platforms and languages.

## Why detect-harness

MCP clients agree on the protocol but not on where or how they store servers.
An installer otherwise needs to independently solve client detection, platform
paths, JSONC comments, TOML tables, YAML lists, collisions, concurrent edits,
permissions, and uninstall behavior.

`detect-harness` provides one reusable implementation:

- **One canonical definition** - a single `StdioServer` becomes valid config for
  every supported client.
- **Evidence-based detection** - present, absent, and unavailable are distinct;
  detection failures are never silently treated as absence.
- **Plan before apply** - inspect changes, show a confirmation UI, then apply the
  exact snapshots that were planned.
- **Safe by default** - same-name foreign entries are conflicts unless the
  caller explicitly opts into replacement.
- **Format aware** - JSON, JSONC, TOML, and YAML receive their required native
  shapes while unrelated settings and comments are retained.
- **Cross-language** - use the Go package directly or invoke the versioned JSON
  companion through typed Node, Python, and Rust wrappers.

## Supported harnesses

| ID | Harness | Config shape |
|---|---|---|
| `claude-desktop` | Claude Desktop | JSON `mcpServers` |
| `claude-code` | Claude Code | JSON `mcpServers` |
| `cursor` | Cursor | JSON `mcpServers` |
| `codex` | Codex CLI | TOML `mcp_servers` |
| `gemini-cli` | Gemini CLI | JSON `mcpServers` |
| `windsurf` | Windsurf | JSON `mcpServers` |
| `zed` | Zed | JSONC `context_servers` |
| `cline` | Cline | JSON `mcpServers` |
| `zoo-code` | Zoo Code ^ | JSON `mcpServers` |
| `amazon-q` | Amazon Q Developer CLI | JSON `mcpServers` |
| `continue` | Continue | YAML `mcpServers` list |
| `opencode` | OpenCode | JSONC `mcp` local command |
| `vscode` | VS Code | JSONC `servers` with stdio type |

^ The deprecated `roo-code` id is accepted as an input alias and resolves to `zoo-code`.

Paths are resolved per platform using the user home, XDG configuration root,
and authoritative Windows application-data directories.

## Core API

### Detect

```go
detections := installer.Detect(ctx)
```

Each result includes a stable harness ID, state, evidence, resolved config path,
and reload hint. Detection can also run without a server definition:

```go
detections, err := detectharness.DetectHarnesses(ctx, detectharness.DetectOptions{})
```

### Plan and apply

```go
plan := installer.Plan(ctx, selected, detectharness.Present, detectharness.PlanOptions{})
changes := plan.Changes()
results := installer.Apply(ctx, plan)
```

Use `detectharness.Absent` to remove the registration. `Installer.Ensure` is a
plan-and-apply convenience method for unattended flows.

### Render without writing

```go
config, err := detectharness.RenderConfig(detectharness.VSCode, server)
```

This is useful for previews, documentation, fixtures, and installers that own
their persistence layer.

### Project scope

Version 0.2 adds directory-local (per-project) configuration support for the 10
harnesses that provide it:

| Harness | Project file | Shareable | Reload hint |
|---|---|---|---|
| Claude Code | `.mcp.json` | Yes (commit) | restart session (approval gate) |
| Cursor | `.cursor/mcp.json` | Yes | restart Cursor |
| Codex CLI | `.codex/config.toml` | Yes (trust gate) | restart Codex |
| Gemini CLI | `.gemini/settings.json` | Yes (trust gate) | `/mcp reload` |
| Zed | `.zed/settings.json` | Yes | live (watched) |
| Zoo Code | `.roo/mcp.json` | Yes | hot-reload (watched) |
| Amazon Q | `.amazonq/mcp.json` | Yes | restart session |
| Continue | `.continue/mcpServers/detect-harness.yaml` | Yes | hot-reload on save |
| OpenCode | `opencode.json` | Yes | restart OpenCode |
| VS Code | `.vscode/mcp.json` | Yes (trust gate) | MCP: List Servers |

Use `detectharness.ProjectScopeDir(dir)` to select a project scope and pass it
through `PlanOptions` or `DetectOptions`:

```go
scope := detectharness.ProjectScopeDir("/path/to/project")

// Project-scoped detection: reports which project files exist.
detections, err := detectharness.DetectHarnesses(ctx, detectharness.DetectOptions{
    Scope: scope,
})

// Project-scoped planning: writes to the project-local config file.
plan := installer.Plan(ctx, selected, detectharness.Present, detectharness.PlanOptions{
    Scope: scope,
})
```

Harnesses without project support (`claude-desktop`, `windsurf`, `cline`) return
`unavailable` / `skipped` — never an error that aborts a multi-harness operation.
The zero-value `Scope` (global) preserves existing behavior exactly.

### Resolve conflicts

The default `ConflictError` policy only updates or removes entries that exactly
match the canonical server definition:

```go
options := detectharness.PlanOptions{
    ConflictPolicy: detectharness.ConflictReplace,
}
```

Use `ConflictReplace` only when your installer owns the stable server name and
intentionally wants to migrate its executable path or environment.

## Companion protocol

The `detect-harness` binary reads exactly one versioned JSON request from stdin
and writes exactly one JSON response to stdout. Secrets never appear in process
arguments.

```bash
printf '%s' '{"version":1,"operation":"detect"}' | detect-harness
```

Operations:

| Operation | Purpose |
|---|---|
| `detect` | Return all harness detections and evidence |
| `render` | Generate one standalone harness configuration |
| `update` | Plan selected additions/removals and optionally apply them |

The machine-readable contract lives in [`protocol/`](protocol/), including
Draft 2020-12 request and response schemas.

## Language wrappers

| Language | Package source | API |
|---|---|---|
| Node / TypeScript | [`wrappers/node`](wrappers/node) | `DetectHarnessClient` |
| Python | [`wrappers/python`](wrappers/python) | `Client` |
| Rust | [`wrappers/rust`](wrappers/rust) | `Client` |

All wrappers expose typed `detect`, `render`, `plan`, and `update` calls. They
resolve an explicit binary path first, then `DETECT_HARNESS_BIN`, then
`detect-harness` on `PATH`. They invoke without a shell, bound process output,
support timeouts, and validate protocol responses.

From a source checkout:

```bash
npm ci --prefix wrappers/node
npm run build --prefix wrappers/node
# Then install /path/to/detect-harness/wrappers/node in your application.
python -m pip install ./wrappers/python
cargo add --path wrappers/rust
```

Every tagged GitHub release contains the npm tarball, Python wheel and source
distribution, Rust crate, protocol schemas, checksums, and native companion
binaries for macOS, Linux, and Windows on AMD64 and ARM64.

## Safety model

- Invalid roots, wrong container types, duplicate JSON keys, oversized configs,
  and multi-document YAML are rejected without writes.
- JSONC comments, TOML source outside managed tables, YAML nodes, and unrelated
  settings are retained.
- Plans retain snapshots and recheck immediately before atomic publication.
- New same-directory temporary files use restrictive permissions; existing
  config permissions are retained, and staged content is flushed before replacement.
- Config targets that are symbolic links are rejected.
- Library operations are serialized by short-lived lock files; locks older than
  five minutes are treated as stale and recovered.
- Multi-harness changes report partial results instead of hiding failures.
- Serialized protocol plans do not include complete user config contents.
- `WriteFileAtomic` creates the parent directory chain if it does not exist, so
  project scopes can write to paths like `.continue/mcpServers/` without
  requiring prior directory setup.
- Deprecated harness IDs (e.g. `"roo-code"`) are accepted as input and resolved
  to their canonical replacement via `CanonicalID`. This ensures protocol-level
  callers deduplicate IDs consistently with the registry.

## Build and test

Requirements: Go 1.22+, Node 18+, Python 3.10+, and Rust 1.71+.

```bash
go test ./...
go vet ./...

npm test --prefix wrappers/node
PYTHONPATH=wrappers/python/src python -m unittest discover -s wrappers/python/tests -v
cargo test --manifest-path wrappers/rust/Cargo.toml
```

Build the companion for the current platform:

```bash
go build -o detect-harness ./cmd/detect-harness
```

## Releases

The root [`package.json`](package.json) is the version authority. Update every
wrapper manifest together with:

```bash
npm run version:set -- 0.2.0
npm run version:check
```

When that version change reaches `main`, the release workflow verifies all
manifests, creates `v<version>`, runs the full test suite, and publishes one
GitHub Release containing every binary and wrapper format. A manually pushed
version tag must match the root version and point to a commit on `main`.

## Transport support

Protocol version 1 manages local stdio MCP servers. Remote HTTP and SSE transports need
client-specific capability handling and are intentionally not implied by the
current API.

## License

MIT. Copyright © 2026 [Łael Al-Halawani](mailto:laelhalawani@gmail.com).
