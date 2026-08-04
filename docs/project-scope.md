# Project-scoped MCP configuration (v0.2.0 design)

Status: **implemented** in v0.2.0. Non-breaking, additive change.

## Goal

detect-harness today detects and edits **global** (system/user) MCP
configurations. Many harnesses also support **per-project** (directory-scoped)
MCP configs — e.g. Claude Code's `.mcp.json`. Per-project configs let a user
keep different MCP servers (and different API keys) per project.

This feature adds the ability to detect and mutate project-scoped configs for
every harness that supports them, behind an opt-in `Scope`, without changing
existing global behavior or breaking any caller.

## Non-breaking contract

- Go library: only **adds** types, fields, and options. Zero values preserve
  today's global behavior. No existing symbol is renamed, removed, or changes
  semantics.
- Protocol (companion binary + wrappers): adds **optional** request/response
  fields. Wire `version` stays `1`. An old client against a new binary ignores
  `scope` and behaves globally; a new client against an old binary sends `scope`
  and the old binary ignores it (returns global results).
- Catalog (`Supported()`): gains an optional `Project` field; global-only
  harnesses omit it.

## Support matrix (13 harnesses)

Sources: official docs plus the config-loading source code of each tool (see
end of file). `Pre-create` = "is it safe for our library to create the project
file before the tool ever runs in that directory?"

| Harness | Project? | Project file (in project dir) | Hold key | Discovery | Merge with global | Pre-create safe? | Reload |
|---|---|---|---|---|---|---|---|
| claude-desktop | **No** | — | `mcpServers` | — | — | n/a | restart app |
| claude-code | Yes | `.mcp.json` | `mcpServers` | cwd | per-name winner (Local>Project>User; **no field merge**) | Yes (approval gate on first run) | restart session |
| cursor | Yes | `.cursor/mcp.json` | `mcpServers` | cwd | additive union; project wins on name clash | Yes | restart Cursor |
| codex | Yes | `.codex/config.toml` | `[mcp_servers.<id>]` | **project_root → cwd walk** | deep per-field; closest-to-cwd wins | Yes (trust gate) | restart Codex |
| gemini-cli | Yes | `.gemini/settings.json` | `mcpServers` | cwd only | deep per-field; project wins | Yes (trust gate) | `/mcp reload` or restart |
| windsurf | **No** | — | `mcpServers` | — | — | n/a | refresh / restart |
| zed | Yes | `.zed/settings.json` | `context_servers` | cwd | per-key deep; project wins | Yes | **live** (settings file watched) |
| cline | **No** | — | `mcpServers` | — | — | n/a | watched (global only) |
| zoo-code | Yes | `.roo/mcp.json` | `mcpServers` | 1st workspace folder | additive; project wins per-name | Yes | **hot-reload** (file watched) |
| amazon-q | Yes | `.amazonq/mcp.json` | `mcpServers` | cwd only | additive per-server; workspace wins | Yes (`q mcp add` even auto-creates empty) | restart session |
| continue | Yes | `.continue/mcpServers/*.{yaml,json}` (a **directory of block files**) | `mcpServers` (YAML **list**) | workspace dirs | concat + dedupe by name | Yes | save → hot-reload |
| opencode | Yes | `opencode.json` / `opencode.jsonc` | `mcp` (object) | **cwd → git-root walk** | deep per-key; project wins | Yes | restart OpenCode |
| vscode | Yes | `.vscode/mcp.json` | `servers` (+`inputs`) | cwd | additive union | Yes (trust gate) | code-lens / `MCP: List Servers` restart |

**10 of 13** harnesses support project-scoped config. `claude-desktop`,
`windsurf`, and `cline` are global-only and will report `unavailable` for
project scope.

Note: **Roo Code** was archived / discontinued by its maintainer in May 2026.
We keep supporting its `.roo/mcp.json` (and the community fork follows the same
convention), but it is flagged in the catalog.

## Per-harness lifecycle notes

- **claude-code**: `.mcp.json` is created manually or by
  `claude mcp add --scope project` (create-or-update, not overwrite). Never
  auto-created by a bare `claude` run. On first session in a dir, Claude Code
  reads it and prompts the user to **approve** each project server (a cloned
  repo cannot auto-run servers). Approval state lives in `~/.claude.json` or in
  `.claude/settings.json` (`enableAllProjectMcpServers`). In an **untrusted**
  folder (workspace-trust dialog) committed approval flags are ignored until
  the user trusts the workspace. Reload = **restart the session** (read at
  session start; no hot-reload of the server list).
- **cursor**: project file created manually / UI / `cursor mcp add`. Not
  auto-created on workspace open. Reload = **restart Cursor**. Streamable-HTTP
  transport requires Cursor v0.48.0+.
- **codex**: project file **never written by Codex** (read-only). Enabled only
  after the project is **trusted**; until then servers are parsed but disabled.
  Codex walks `.codex/config.toml` from the project root down to cwd and
  **deep-merges** all layers (closest-to-cwd wins per field). Project config
  cannot change provider/auth keys (they are stripped with a warning).
- **gemini-cli**: project file created manually or via `gemini mcp add`. Read
  on load; not overwritten. Workspace settings are skipped when the workspace
  is the home dir; a trust check gates whether they apply. Reload = `/mcp
  reload` in session, else restart.
- **zed**: project `.zed/settings.json` overrides global per-key (deep). Zed
  **watches settings files live** — edits apply without a restart; changing a
  server entry restarts that server. A worktree-trust prompt on first open is
  independent of the file.
- **zoo-code**: `<workspace>/.roo/mcp.json`. Global file lives in VS Code
  `globalStorage` `ZooCodeOrganization.zoo-code` (not `~/.roo/`); Zoo Code never
  writes defaults into the project file. **Hot-reloaded** via a
  `FileSystemWatcher` (debounced ~500 ms).
- **amazon-q**: `.amazonq/mcp.json` (legacy, loaded by default via the built-in
  agent's `useLegacyMcpJson: true`). `q mcp add --scope workspace` auto-creates
  `{ "mcpServers": {} }` if missing, otherwise reads → mutates → saves
  (preserving content). Additive per-server; workspace wins on name conflict.
  Reload = restart the chat session.
- **continue**: project MCP is a **directory of block files**
  `.continue/mcpServers/*.{yaml,json}`, each a single server or a
  `{mcpServers:{...}}` map; concatenated and deduped by name. (A project
  `.continue/config.yaml` is **not** a supported merge source — `config.yaml`
  is global only.) Hot-reloaded on save. Secrets via `${{ secrets.NAME }}`.
- **opencode**: project `opencode.json`/`.jsonc` merged **deep per-key** with
  global (`~/.config/opencode/opencode.json`); project wins. OpenCode walks
  cwd → nearest git root and merges all configs found. Local-command shape uses
  `command` as an **array** and `environment` (not `env`). Reload = restart.
- **vscode**: `.vscode/mcp.json` with top-level `servers` (+ optional `inputs`
  for `${input:id}` secrets). First server start shows a **trust** dialog
  (reset via `MCP: Reset Trust`). Reload via code-lenses in `mcp.json` or
  `MCP: List Servers → restart`; `chat.mcp.autostart` auto-restarts on change.
  No window reload required. Shipped in VS Code 1.99 (April 2025), GA now.

## Cross-cutting findings

- **Preemptive creation is safe for all 10 supported harnesses.** None
  overwrite or inject defaults into an existing project file on launch; all
  read it as-is. Three of them gate the result behind a **trust/approval**
  dialog (claude-code, codex, vscode) and two more behind a trust check
  (gemini-cli, and zed's separate worktree-trust). Our docs must surface this
  so consumers can tell their users "approve/trust the workspace".
- **Discovery differs and we will NOT replicate it.** Most harnesses read the
  project file from the **cwd only**; codex walks project_root→cwd and opencode
  walks cwd→git-root, both deep-merging layers. Our library will write a
  **single deterministic file** in the directory the caller specifies, and the
  catalog documents each tool's own walk/merge so the consumer understands
  precedence. This keeps us a predictable "library + information" layer.
- **Format/key is identical to global** for every supported harness (we already
  render/mutate each format). Continue is the lone structural exception:
  project scope is a *directory of block files* rather than one file.
- **Reload varies widely** (live/hot-reload for zed, roo, continue; restart for
  the rest; `/mcp reload` for gemini). The catalog exposes a per-scope
  `ReloadHint`.
- **Secrets**: prefer each tool's env/interpolation (`${VAR}`, `${env:VAR}`,
  `${{ secrets.NAME }}`, `{env:VAR}`) and keep real values out of VCS. Several
  project files are meant to be committed (claude-code, cursor, zed, vscode,
  opencode) while others are local-only.

## Go API

### Scope

```go
// Scope selects where configuration is detected and applied.
// The zero value is global scope (preserves v0.1.x behavior).
type Scope struct {
    Mode ScopeMode
    Dir  string // absolute project directory; required when Mode == ScopeProject
}

type ScopeMode string

const (
    ScopeGlobal  ScopeMode = ""         // zero value → current behavior
    ScopeProject ScopeMode = "project"
)

// ProjectScopeDir builds a project scope targeting dir.
func ProjectScopeDir(dir string) Scope { return Scope{Mode: ScopeProject, Dir: dir} }
```

### Options

`DetectOptions` and `PlanOptions` carry an optional `Scope` field. Zero value
means global (unchanged from v0.1.x). `Ensure`, `Plan`, `Apply`, and `Detect`
are scope-aware through these options.

### Catalog metadata

`Harness` has an optional `Project *ProjectScope` field (nil for global-only):

```go
type ProjectScope struct {
    Path       string   // relative path within the project dir, e.g. ".mcp.json"
    ReloadHint string   // may differ from global
    Lifecycle  string   // creation / merge / preemptive-create behavior (human-readable)
    Shareable  bool     // intended to be committed to VCS
    TrustGate  bool     // requires workspace trust/approval before servers load
}
```

### Detection result

`Detection`, `Change`, and `Result` carry optional `Scope ScopeMode` and
`ScopeDir string` fields (omitted when global), so consumers can distinguish a
global hit from a project hit.

### Rendering

`RenderConfig(harness, server)` renders a global config. `RenderConfigScoped(harness, server,
scope)` renders a config for the given scope. Project scope returns the rendered
project file for that harness (`Continue` produces a bare block file without
config.yaml metadata — see Locked decisions).

### Unsupported scope

For global-only harnesses (`claude-desktop`, `windsurf`, `cline`), project-scope
`Detect` returns state `unavailable`, and `Plan`/`Apply` produce a `skipped`
change with a clear `Reason`. Never an error that aborts a multi-harness run.

### Protocol / wrappers

The optional `scope: {mode, dir}` request field and optional `scope`,
`scopeDir`, and `project` response fields are additive — `version` remains `1`.
An older companion binary ignores the `scope` field and returns global results.

## Locked decisions

1. **Discovery model — single deterministic file.** We write/read one file in
   the caller-supplied directory, uniformly for all 10 supported harnesses, and
   document each tool's own parent-walk/deep-merge in the catalog so consumers
   understand precedence. We do **not** replicate codex/opencode walks.
2. **Continue project scope — canonical block file.** Target
   `.continue/mcpServers/detect-harness.yaml` as the one deterministic file;
   Continue still merges it with any sibling block files the user has.
3. **Roo Code → Zoo Code (repoint).** Zoo Code (`ZooCodeOrganization.zoo-code`)
   is the community successor that took over after the Roo team discontinued the
   extension in May 2026. Verified against Zoo Code's source:
   - Project file is **still `.roo/mcp.json`** (unchanged), key `mcpServers`.
   - Global file is still `mcp_settings.json`, now under globalStorage
     `ZooCodeOrganization.zoo-code` (was `rooveterinaryinc.roo-cline`).
   - Detection extension prefix becomes `ZooCodeOrganization.zoo-code-`
     (was `rooveterinaryinc.roo-cline-`).
   - Canonical harness ID becomes **`zoo-code`** (Name "Zoo Code"); the existing
     `roo-code` ID is retained as a **deprecated alias** resolving to the same
     definition, so current callers keep working (non-breaking). `Supported()`
   lists "Zoo Code"; `roo-code` is documented as a legacy alias.

## Sources

- Claude Code: https://docs.claude.com/en/docs/claude-code/mcp ,
  https://docs.claude.com/en/docs/claude-code/settings ,
  https://docs.claude.com/en/docs/claude-code/managed-mcp
- Claude Desktop: https://modelcontextprotocol.io/quickstart/user
- Cursor: https://docs.cursor.com/docs/mcp ,
  https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-cursor.md
- Codex: https://developers.openai.com/codex/config-advanced ,
  https://developers.openai.com/codex/config-reference ,
  https://github.com/openai/codex (config/src/loader, config/src/merge)
- Gemini CLI: https://geminicli.com/docs/reference/configuration ,
  https://geminicli.com/docs/tools/mcp-server ,
  https://github.com/google-gemini/gemini-cli (config/settings.ts, utils/deepMerge.ts)
- Windsurf: https://docs.windsurf.com/windsurf/cascade/mcp
- Zed: https://zed.dev/docs/ai/mcp , https://zed.dev/docs/ai/agent-settings ,
  https://zed.dev/docs/migrate/vs-code
- Cline: https://docs.cline.bot/mcp/mcp-overview.md ,
  https://docs.cline.bot/getting-started/config.md
- Roo Code: https://roocodeinc.github.io/Roo-Code/features/mcp/using-mcp-in-roo ,
  https://github.com/RooCodeInc/Roo-Code (src/services/mcp/McpHub.ts)
- Amazon Q: https://docs.aws.amazon.com/amazonq/latest/qdeveloper-ug/command-line-mcp.html ,
  https://github.com/aws/amazon-q-developer-cli (chat-cli/src/cli/mcp.rs, agent/mod.rs)
- Continue: https://docs.continue.dev/reference ,
  https://docs.continue.dev/customize/deep-dives/mcp ,
  https://github.com/continuedev/continue (core/config/load.ts, core/context/mcp/json)
- OpenCode: https://opencode.ai/docs/config/ , https://opencode.ai/docs/mcp-servers/ ,
  https://github.com/anomalyco/opencode
- VS Code: https://code.visualstudio.com/docs/agent-customization/mcp-servers ,
  https://code.visualstudio.com/docs/agents/reference/mcp-configuration
