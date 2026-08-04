#!/usr/bin/env node
import { spawn } from "node:child_process";
import process from "node:process";

const treeMode = process.env.DETECT_HARNESS_FAKE_MODE;
if (treeMode === "hang-tree" || treeMode === "overflow-tree") {
  process.on("SIGTERM", () => {});
  spawn(process.execPath, ["-e", "process.on('SIGTERM', () => {}); setInterval(() => {}, 1000)"], {
    stdio: ["ignore", "inherit", "inherit"],
  });
  if (treeMode === "overflow-tree") process.stdout.write("x".repeat(4096));
  setInterval(() => {}, 1000);
  await new Promise(() => {});
}

if (process.argv.length !== 2) {
  process.stdout.write(JSON.stringify({ version: 1, ok: false, error: { code: "unexpected_args", message: "arguments supplied" } }));
  process.exitCode = 2;
} else {
  let input = "";
  for await (const chunk of process.stdin) input += chunk;
  const request = JSON.parse(input);
  const mode = request.server?.name;

  if (mode === "protocol-error") {
    process.stdout.write(JSON.stringify({ version: 1, ok: false, error: { code: "operation_failed", message: "fake failure" } }));
    process.exitCode = 1;
  } else if (mode === "wrong-version") {
    process.stdout.write(JSON.stringify({ version: 2, ok: true, config: "bad" }));
  } else if (mode === "overflow") {
    process.stdout.write("x".repeat(4096));
  } else if (request.operation === "detect") {
    const detection = { id: "cursor", name: "Cursor", reloadHint: "Reload Cursor", state: "present", evidence: ["/fake/cursor"] };
    if (request.scope?.mode === "project") {
      detection.scope = "project";
      detection.scopeDir = request.scope.dir;
    }
    process.stdout.write(JSON.stringify({ version: 1, ok: true, detections: [detection] }));
  } else if (request.operation === "render") {
    process.stdout.write(JSON.stringify({ version: 1, ok: true, config: JSON.stringify({ harness: request.harness, server: request.server }) }));
  } else if (request.operation === "update") {
    const scopeFields = request.scope?.mode === "project"
      ? { scope: "project", scopeDir: request.scope.dir }
      : {};
    const changes = request.harnesses.map((harnessId) => ({
      harnessId,
      name: harnessId,
      desired: request.desired,
      state: request.dryRun ? "noop" : "ready",
      ...(request.dryRun ? { reason: request.conflictPolicy } : { action: "add" }),
      ...scopeFields,
      before: "obsolete",
      after: "obsolete",
    }));
    const response = { version: 1, ok: true, changes };
    if (!request.dryRun) {
      response.results = changes.map(({ harnessId, name, desired, action }) => ({
        harnessId,
        name,
        desired,
        action,
        state: "applied",
      }));
    }
    process.stdout.write(JSON.stringify(response));
  }
}
