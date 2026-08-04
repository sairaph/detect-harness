import assert from "node:assert/strict";
import { chmod, mkdtemp, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  DetectHarnessClient,
  InvocationError,
  OutputLimitError,
  ProtocolError,
  ProtocolValidationError,
  projectScope,
} from "../dist/index.js";

const fakeBinary = fileURLToPath(new URL("./fake-binary.mjs", import.meta.url));
await chmod(fakeBinary, 0o755);

const server = { name: "example", command: "example-mcp", args: ["--stdio"], env: { TOKEN: "secret" } };

test("detect, render, plan, and update use protocol v1", async () => {
  const client = new DetectHarnessClient({ binaryPath: fakeBinary });

  assert.equal((await client.detect())[0].id, "cursor");
  assert.deepEqual(JSON.parse(await client.render("cursor", server)), { harness: "cursor", server });

  const changes = await client.plan(["cursor"], "present", server, { conflictPolicy: "replace" });
  assert.equal(changes[0].state, "noop");
  assert.equal(changes[0].reason, "replace");
  assert.equal(Object.hasOwn(changes[0], "before"), false);
  assert.equal(Object.hasOwn(changes[0], "after"), false);
  assert.equal((await client.plan(["cursor"], "present", server))[0].reason, "error");

  const outcome = await client.update(["cursor"], "present", server);
  assert.equal(outcome.changes[0].action, "add");
  assert.equal(outcome.results[0].state, "applied");
});

test("binary resolution uses DETECT_HARNESS_BIN", { concurrency: false }, async () => {
  const previous = process.env.DETECT_HARNESS_BIN;
  process.env.DETECT_HARNESS_BIN = fakeBinary;
  try {
    assert.equal((await new DetectHarnessClient().detect()).length, 1);
  } finally {
    if (previous === undefined) delete process.env.DETECT_HARNESS_BIN;
    else process.env.DETECT_HARNESS_BIN = previous;
  }
});

test("binary resolution falls back to PATH", { concurrency: false }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "detect-harness-node-"));
  await symlink(fakeBinary, join(directory, "detect-harness"));
  const previousBin = process.env.DETECT_HARNESS_BIN;
  const previousPath = process.env.PATH;
  delete process.env.DETECT_HARNESS_BIN;
  process.env.PATH = `${directory}${delimiter}${previousPath ?? ""}`;
  try {
    assert.equal((await new DetectHarnessClient().detect())[0].name, "Cursor");
  } finally {
    if (previousBin !== undefined) process.env.DETECT_HARNESS_BIN = previousBin;
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
  }
});

test("structured protocol errors retain their code", async () => {
  const client = new DetectHarnessClient({ binaryPath: fakeBinary });
  await assert.rejects(
    client.render("cursor", { name: "protocol-error", command: "fake" }),
    (error) => error instanceof ProtocolError && error.code === "operation_failed" && error.exitCode === 1,
  );
});

test("protocol version and output limits are enforced", async () => {
  const client = new DetectHarnessClient({ binaryPath: fakeBinary });
  await assert.rejects(
    client.render("cursor", { name: "wrong-version", command: "fake" }),
    ProtocolValidationError,
  );
  await assert.rejects(
    new DetectHarnessClient({ binaryPath: fakeBinary, maxOutputBytes: 256 }).render(
      "cursor",
      { name: "overflow", command: "fake" },
    ),
    OutputLimitError,
  );
});

test("timeout covers request writing and kills the POSIX process group", { concurrency: false, skip: process.platform === "win32" }, async () => {
  const previous = process.env.DETECT_HARNESS_FAKE_MODE;
  process.env.DETECT_HARNESS_FAKE_MODE = "hang-tree";
  const started = Date.now();
  try {
    await assert.rejects(
      new DetectHarnessClient({ binaryPath: fakeBinary, timeoutMs: 50 }).render("cursor", {
        name: "example",
        command: "fake",
        env: { LARGE: "x".repeat(512 * 1024) },
      }),
      InvocationError,
    );
    assert.ok(Date.now() - started < 2000);
  } finally {
    if (previous === undefined) delete process.env.DETECT_HARNESS_FAKE_MODE;
    else process.env.DETECT_HARNESS_FAKE_MODE = previous;
  }
});

test("output-limit termination escalates for a resistant process group", { concurrency: false, skip: process.platform === "win32" }, async () => {
  const previous = process.env.DETECT_HARNESS_FAKE_MODE;
  process.env.DETECT_HARNESS_FAKE_MODE = "overflow-tree";
  const started = Date.now();
  try {
    await assert.rejects(
      new DetectHarnessClient({ binaryPath: fakeBinary, maxOutputBytes: 256 }).detect(),
      OutputLimitError,
    );
    assert.ok(Date.now() - started < 2000);
  } finally {
    if (previous === undefined) delete process.env.DETECT_HARNESS_FAKE_MODE;
    else process.env.DETECT_HARNESS_FAKE_MODE = previous;
  }
});

test("project scope flows into requests and responses, and zoo-code is accepted", async () => {
  const client = new DetectHarnessClient({ binaryPath: fakeBinary });
  const scope = projectScope("/tmp/project");

  const detection = (await client.detect(scope))[0];
  assert.equal(detection.scope, "project");
  assert.equal(detection.scopeDir, "/tmp/project");

  const changes = await client.plan(["zoo-code"], "present", server, { scope });
  assert.equal(changes[0].scope, "project");
  assert.equal(changes[0].scopeDir, "/tmp/project");
  assert.equal(changes[0].harnessId, "zoo-code");

  assert.throws(() => projectScope("  "), TypeError);
});
