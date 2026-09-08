# Project-scoped configuration

detect-harness manages MCP server registrations in two scopes. Global scope
targets each harness's system or user configuration. Project scope targets a
directory-local configuration inside a caller-supplied project directory, for
the harnesses that read one. Project scope lets a user keep different MCP
servers, and different secrets, per project.

The zero-value `Scope` selects global scope. `ProjectScopeDir(dir)` selects
project scope; `DetectOptions` and `PlanOptions` carry it, and `Detection`,
`Change`, and `Result` echo it back through their `Scope` and `ScopeDir`
fields so a project hit is distinguishable from a global one. The catalog
returned by `Supported()` exposes a `Project` field with the project file path,
reload hint, lifecycle summary, and whether the file is meant to be committed
or gated behind a trust prompt; it is nil for global-only harnesses.

## Support matrix

Sources: each tool's official documentation and its configuration-loading
source code (listed at the end). "Pre-create safe" means the tool reads a
project file that exists before its first run in that directory without
overwriting it.

| Harness | Project? | Project file | Key | Discovery | Merge with global | Pre-create safe | Reload |
|---|---|---|---|---|---|---|---|
| claude-desktop | No | — | `mcpServers` | — | — | n/a | restart app |
| claude-code | Yes | `.mcp.json` | `mcpServers` | cwd | per-name winner (local > project > user; no field merge) | Yes (approval gate on first run) | restart session |
| cursor | Yes | `.cursor/mcp.json` | `mcpServers` | cwd | additive union; project wins on name clash | Yes | restart Cursor |
| codex | Yes | `.codex/config.toml` | `[mcp_servers.<id>]` | project root → cwd walk | deep per-field; closest to cwd wins | Yes (trust gate) | restart Codex |
| gemini-cli | Yes | `.gemini/settings.json` | `mcpServers` | cwd only | deep per-field; project wins | Yes (trust gate) | `/mcp reload` or restart |
| windsurf | No | — | `mcpServers` | — | — | n/a | refresh or restart |
| zed | Yes | `.zed/settings.json` | `context_servers` | cwd | per-key deep; project wins | Yes | live (settings file watched) |
| cline | No | — | `mcpServers` | — | — | n/a | watched (global only) |
| zoo-code | Yes | `.roo/mcp.json` | `mcpServers` | first workspace folder | additive; project wins per name | Yes | hot-reload (file watched) |
| amazon-q | Yes | `.amazonq/mcp.json` | `mcpServers` | cwd only | additive per server; workspace wins | Yes (`q mcp add` auto-creates an empty file) | restart session |
| continue | Yes | `.continue/mcpServers/*.{yaml,json}` (a directory of block files) | `mcpServers` (YAML list) | workspace dirs | concatenate, dedupe by name | Yes | hot-reload on save |
| opencode | Yes | `opencode.jsonc` (a lone existing `opencode.json` is honored) | `mcp` (object) | git root → cwd walk, then `.opencode/` dirs | deep per-key; later layer wins | Yes | restart OpenCode |
| vscode | Yes | `.vscode/mcp.json` | `servers` (+ `inputs`) | cwd | additive union | Yes (trust gate) | code lens or `MCP: List Servers` |

Ten of the thirteen harnesses support project scope. `claude-desktop`,
`windsurf`, and `cline` are global-only: project-scope detection reports them
as `unavailable`, and project-scope plans produce a `skipped` result with a
reason rather than an error that aborts a multi-harness operation.

## Per-harness notes

- **claude-code**: `.mcp.json` is created manually or by
  `claude mcp add --scope project`, which creates or updates without
  overwriting. A bare `claude` run never creates it. On the first session in a
  directory Claude Code prompts the user to approve each project server, since
  a cloned repository must not auto-run servers. Approval state lives in
  `~/.claude.json` or in `.claude/settings.json`
  (`enableAllProjectMcpServers`). In an untrusted folder committed approval
  flags are ignored until the user trusts the workspace. The server list is
  read at session start, so reload means restarting the session.
- **cursor**: the project file is created manually, through the MCP tools UI,
  or by `cursor mcp add`. It is not auto-created on workspace open. Reload
  means restarting Cursor.
- **codex**: Codex never writes the project file. Project servers are enabled
  only after the project is trusted; until then they are parsed but disabled.
  Codex walks `.codex/config.toml` from the project root down to cwd and
  deep-merges every layer, closest to cwd winning per field. Project
  configuration cannot change provider or auth keys; those are stripped with a
  warning.
- **gemini-cli**: the project file is created manually or by
  `gemini mcp add`. It is read on load and not overwritten. Workspace settings
  are skipped when the workspace is the home directory, and a trust check
  gates whether they apply. Reload with `/mcp reload` in session, otherwise
  restart.
- **zed**: `.zed/settings.json` overrides global settings per key. Zed watches
  settings files, so edits apply without a restart and changing a server entry
  restarts that server. A separate worktree-trust prompt may appear on first
  open.
- **zoo-code**: `<workspace>/.roo/mcp.json`. The global file lives in VS Code
  `globalStorage` under `ZooCodeOrganization.zoo-code`, not `~/.roo/`. Zoo Code
  never writes defaults into the project file and hot-reloads it through a
  `FileSystemWatcher` (debounced about 500 ms). Zoo Code is the community
  successor of the discontinued Roo Code extension and keeps its `.roo/`
  convention; the `roo-code` id remains accepted as an alias of `zoo-code`.
- **amazon-q**: `.amazonq/mcp.json` is loaded by default through the built-in
  agent's `useLegacyMcpJson: true`. `q mcp add --scope workspace` creates
  `{ "mcpServers": {} }` when the file is missing and otherwise reads,
  mutates, and saves it, preserving content. Merging is additive per server
  with the workspace winning on name conflicts. Reload means restarting the
  chat session.
- **continue**: project MCP configuration is a directory of block files,
  `.continue/mcpServers/*.{yaml,json}`, each holding a single server or a
  `{mcpServers: {...}}` map; they are concatenated and deduped by name. A
  project `.continue/config.yaml` is not a merge source; `config.yaml` is
  global only. Changes hot-reload on save. Secrets use `${{ secrets.NAME }}`.
- **opencode**: within a directory OpenCode loads `opencode.json` then
  `opencode.jsonc`, deep-merging per key so the later file wins. The global
  directory is `<xdg config>/opencode` on every platform (xdg-basedir, so
  `%USERPROFILE%\.config\opencode` on Windows, never `%APPDATA%`), and holds
  three files merged in order: `config.json` (kept for older installs),
  `opencode.json`, `opencode.jsonc`. On first run without any of them OpenCode
  seeds `opencode.jsonc`, which is why it is the file detect-harness creates
  and prefers. The full merge order, lowest precedence first, is: global files,
  `$OPENCODE_CONFIG`, project files walked from the git root to cwd, every
  `.opencode/` directory on that walk, `~/.opencode/`, `$OPENCODE_CONFIG_DIR`,
  `$OPENCODE_CONFIG_CONTENT`. `OPENCODE_DISABLE_PROJECT_CONFIG=1` skips project
  files. The local-command shape uses `command` as an array and `environment`
  rather than `env`. Reload means restarting OpenCode. Verified against
  `anomalyco/opencode` (`packages/core/src/global.ts`,
  `packages/opencode/src/config/{config,paths}.ts`).
- **vscode**: `.vscode/mcp.json` with a top-level `servers` object and an
  optional `inputs` list for `${input:id}` secrets. The first server start
  shows a trust dialog, resettable with `MCP: Reset Trust`. Reload through the
  code lenses in `mcp.json` or `MCP: List Servers`, then restart;
  `chat.mcp.autostart` restarts servers automatically on change. No window
  reload is required.

## Cross-cutting behavior

- **Pre-creation is safe for all ten supported harnesses.** None overwrite or
  inject defaults into an existing project file on launch. Three gate the
  result behind a trust or approval dialog (claude-code, codex, vscode) and
  two more behind a trust check (gemini-cli, and zed's worktree trust). The
  catalog's `TrustGate` flag carries this so installers can tell users to
  approve or trust the workspace.
- **One deterministic file per harness.** detect-harness writes and reads a
  single file in the directory the caller supplies, uniformly for all ten
  harnesses. It does not replicate the parent-directory walks of codex and
  opencode; the catalog documents each tool's own discovery and merge rules so
  consumers understand precedence.
- **Same format and key as global scope** for every supported harness, so the
  same renderers apply. Continue is the one structural exception: its project
  scope is a directory of block files, and detect-harness targets the single
  canonical block file `.continue/mcpServers/detect-harness.yaml`, rendered
  without the `name`, `version`, and `schema` metadata that only the global
  `config.yaml` carries. Continue merges it with any sibling block files.
- **OpenCode layers are inspected, never written.** The managed file is
  `opencode.jsonc`, or a lone existing `opencode.json`. The other layers
  OpenCode merges in the same scope (`config.json`, the sibling
  `opencode.json`, `.opencode/` directories, `~/.opencode/`,
  `$OPENCODE_CONFIG`, `$OPENCODE_CONFIG_DIR`, `$OPENCODE_CONFIG_CONTENT`) are
  read-only conflict sources: a same-name entry there is a conflict under
  `ConflictError` and a `Reason` warning under `ConflictReplace`. Global layers
  are not conflict sources for project scope, because differing per-project
  settings are the purpose of project scope. Existing layer files appear as
  detection evidence.
- **Reload varies.** Zed, Zoo Code, and Continue pick up changes live; Gemini
  CLI offers `/mcp reload`; the rest need a restart. The catalog exposes a
  per-scope `ReloadHint`.
- **Secrets.** Prefer each tool's own interpolation (`${VAR}`, `${env:VAR}`,
  `${{ secrets.NAME }}`, `{env:VAR}`) and keep real values out of version
  control. The `Shareable` flag marks files intended to be committed.

## Protocol

The request carries an optional `scope: {mode, dir}` object, and detections,
changes, and results carry optional `scope` and `scopeDir` fields; detections
also carry the catalog's `project` metadata. Wire `version` remains `1`, and a
request without `scope` is a global request.

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
- Zoo Code (and the Roo Code lineage it continues):
  https://roocodeinc.github.io/Roo-Code/features/mcp/using-mcp-in-roo ,
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
