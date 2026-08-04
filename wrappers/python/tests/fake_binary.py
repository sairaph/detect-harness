#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import sys
import time

tree_mode = os.environ.get("DETECT_HARNESS_FAKE_MODE")
if tree_mode in ("hang-tree", "overflow-tree"):
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    subprocess.Popen(
        [
            sys.executable,
            "-c",
            "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)",
        ],
        stdin=subprocess.DEVNULL,
    )
    if tree_mode == "overflow-tree":
        sys.stdout.write("x" * 4096)
        sys.stdout.flush()
    time.sleep(60)

if len(sys.argv) != 1:
    print(json.dumps({"version": 1, "ok": False, "error": {"code": "unexpected_args", "message": "arguments supplied"}}))
    raise SystemExit(2)

request = json.load(sys.stdin)
mode = request.get("server", {}).get("name")

if mode == "protocol-error":
    print(json.dumps({"version": 1, "ok": False, "error": {"code": "operation_failed", "message": "fake failure"}}))
    raise SystemExit(1)
if mode == "wrong-version":
    print(json.dumps({"version": 2, "ok": True, "config": "bad"}))
elif mode == "overflow":
    sys.stdout.write("x" * 4096)
elif request["operation"] == "detect":
    detection = {"id": "cursor", "name": "Cursor", "reloadHint": "Reload Cursor", "state": "present", "evidence": ["/fake/cursor"]}
    scope = request.get("scope")
    if isinstance(scope, dict) and scope.get("mode") == "project":
        detection["scope"] = "project"
        detection["scopeDir"] = scope.get("dir")
    print(json.dumps({"version": 1, "ok": True, "detections": [detection]}))
elif request["operation"] == "render":
    print(json.dumps({"version": 1, "ok": True, "config": json.dumps({"harness": request["harness"], "server": request["server"]})}))
elif request["operation"] == "update":
    scope = request.get("scope")
    scope_fields = (
        {"scope": "project", "scopeDir": scope.get("dir")}
        if isinstance(scope, dict) and scope.get("mode") == "project"
        else {}
    )
    changes = [
        {
            "harnessId": harness,
            "name": harness,
            "desired": request["desired"],
            "state": "noop" if request.get("dryRun") else "ready",
            **(
                {"reason": request.get("conflictPolicy", "")}
                if request.get("dryRun")
                else {"action": "add"}
            ),
            **scope_fields,
            "before": "obsolete",
            "after": "obsolete",
        }
        for harness in request["harnesses"]
    ]
    response = {"version": 1, "ok": True, "changes": changes}
    if not request.get("dryRun"):
        response["results"] = [
            {"harnessId": change["harnessId"], "name": change["name"], "desired": change["desired"], "state": "applied", "action": change["action"]}
            for change in changes
        ]
    print(json.dumps(response))
