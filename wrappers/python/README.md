# detect-harness for Python

Typed Python 3.10+ bindings for the `detect-harness` companion binary. The
package uses only the standard library at runtime.

```python
from detect_harness import Client, StdioServer

client = Client()
server = StdioServer("example", "npx", ("example-mcp",))

detections = client.detect()
config = client.render("cursor", server)
changes = client.plan(("cursor", "vscode"), "present", server)
outcome = client.update(("cursor", "vscode"), "present", server)
```

Pass `binary_path` to `Client` to select the executable. Otherwise the client
uses `DETECT_HARNESS_BIN`, then resolves `detect-harness` on `PATH`. Requests are
sent as one JSON document on stdin and no shell is used.

The combined stdout/stderr limit defaults to 32 MiB and can be changed with
`max_output_bytes`. Set `timeout` to bound the full invocation, including
request writing. On POSIX, timeout and output-limit failures terminate the
companion process group with escalation. Protocol failures raise
`ProtocolError`; its `code` attribute contains the structured companion error
code. Invocation and malformed-response failures derive from
`DetectHarnessError`.

`update` returns an `UpdateOutcome`; its `changes` are plans and its `results`
contain one `UpdateResult` per selected harness. The default conflict policy is
`error`; pass `conflict_policy="replace"` explicitly to replace conflicts.
