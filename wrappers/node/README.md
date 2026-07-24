# detect-harness for Node.js

Typed ESM bindings for the `detect-harness` companion binary. Node.js 18 or
newer is required; the package has no runtime dependencies.

```ts
import { DetectHarnessClient } from "@sairaph/detect-harness";

const client = new DetectHarnessClient();
const server = { name: "example", command: "npx", args: ["example-mcp"] };

const detections = await client.detect();
const config = await client.render("cursor", server);
const changes = await client.plan(["cursor", "vscode"], "present", server);
const outcome = await client.update(["cursor", "vscode"], "present", server, {
  conflictPolicy: "replace",
});
```

Pass `binaryPath` to the constructor to select the executable. Otherwise the
client uses `DETECT_HARNESS_BIN`, then resolves `detect-harness` on `PATH`.
Requests are sent as one JSON document on stdin and no shell is used.

The combined stdout/stderr limit defaults to 32 MiB and can be changed with
`maxOutputBytes`. Set `timeoutMs` to bound the full invocation, including request
writing. On POSIX, timeout and output-limit failures terminate the companion's
process group with escalation. Protocol failures throw `ProtocolError`, whose
`code` contains the companion's structured error code. Invocation and
malformed-response failures throw subclasses of `DetectHarnessError`.

`update` returns an `UpdateOutcome`; its `changes` are plans and its `results`
contain one `UpdateResult` per selected harness. The default conflict policy is
`error`; pass `conflictPolicy: "replace"` explicitly to replace conflicts.
