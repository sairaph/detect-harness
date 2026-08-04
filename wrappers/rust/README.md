# detect-harness Rust wrapper

Thin, strongly typed client for protocol v1 of the `detect-harness` companion
binary. It provides `detect`, `render`, `plan` (a dry-run update), and `update`
without reimplementing harness configuration behavior in Rust.

```toml
[dependencies]
detect-harness = { path = "wrappers/rust" }
```

```rust
use detect_harness::{Client, DesiredState, HarnessId, StdioServer};

let client = Client::new();
let server = StdioServer::new("my-server", "/usr/local/bin/my-server");

let detections = client.detect()?;
let config = client.render(HarnessId::Codex, &server)?;
let preview = client.plan(
    &[HarnessId::Codex],
    DesiredState::Present,
    &server,
)?;
let outcome = client.update(
    &[HarnessId::Codex],
    DesiredState::Present,
    &server,
)?;
# Ok::<(), detect_harness::Error>(())
```

`Client::with_binary` selects an explicit executable. Otherwise the client uses
`DETECT_HARNESS_BIN`, then asks the operating system to resolve
`detect-harness` through `PATH`. The binary is executed directly with no shell
and no arguments; exactly one JSON request is written to stdin. Stdout and
stdout and stderr are limited to 32 MiB combined by default and can be adjusted
with `Client::max_output_bytes`. The default conflict policy is `error`; use
`plan_with_conflict_policy` or `update_with_conflict_policy` to select
`ConflictPolicy::Replace`.

`Client::timeout` adds a std-only timeout for request writing and process
execution. On Unix the companion runs in its own process group (via `setpgid`)
and receives `SIGTERM` followed by `SIGKILL` after a 250 ms grace period,
matching the Node and Python wrappers.

`update` returns an `UpdateOutcome`; its `changes` are plans and its `results`
contain one `UpdateResult` with a `ResultState` per selected harness.
