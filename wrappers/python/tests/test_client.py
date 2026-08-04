import json
import os
import pathlib
import sys
import tempfile
import time
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from detect_harness import (  # noqa: E402
    Client,
    InvocationError,
    OutputLimitError,
    ProjectScope,
    ProtocolError,
    ProtocolValidationError,
    Scope,
    StdioServer,
    project_scope,
)

FAKE_BINARY = pathlib.Path(__file__).with_name("fake_binary.py")


class ClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        FAKE_BINARY.chmod(0o755)

    def setUp(self) -> None:
        self.client = Client(binary_path=FAKE_BINARY)
        self.server = StdioServer("example", "example-mcp", ("--stdio",), {"TOKEN": "secret"})

    def test_all_operations_use_protocol_v1(self) -> None:
        self.assertEqual(self.client.detect()[0].id, "cursor")
        rendered = json.loads(self.client.render("cursor", self.server))
        self.assertEqual(rendered["harness"], "cursor")
        self.assertEqual(rendered["server"]["env"], {"TOKEN": "secret"})

        changes = self.client.plan(("cursor",), "present", self.server, conflict_policy="replace")
        self.assertEqual(changes[0].state, "noop")
        self.assertEqual(changes[0].reason, "replace")
        self.assertFalse(hasattr(changes[0], "before"))
        self.assertFalse(hasattr(changes[0], "after"))
        self.assertEqual(self.client.plan(("cursor",), "present", self.server)[0].reason, "error")

        outcome = self.client.update(("cursor",), "present", self.server)
        self.assertEqual(outcome.changes[0].action, "add")
        self.assertEqual(outcome.results[0].state, "applied")

    def test_environment_binary_resolution(self) -> None:
        with mock.patch.dict(os.environ, {"DETECT_HARNESS_BIN": str(FAKE_BINARY)}):
            self.assertEqual(len(Client().detect()), 1)

    def test_path_binary_resolution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            pathlib.Path(directory, "detect-harness").symlink_to(FAKE_BINARY)
            environment = {
                "PATH": os.pathsep.join((directory, os.environ.get("PATH", ""))),
                "DETECT_HARNESS_BIN": "",
            }
            with mock.patch.dict(os.environ, environment):
                self.assertEqual(Client().detect()[0].name, "Cursor")

    def test_structured_protocol_error_retains_code(self) -> None:
        with self.assertRaises(ProtocolError) as raised:
            self.client.render("cursor", StdioServer("protocol-error", "fake"))
        self.assertEqual(raised.exception.code, "operation_failed")
        self.assertEqual(raised.exception.exit_code, 1)

    def test_version_and_output_limits_are_enforced(self) -> None:
        with self.assertRaises(ProtocolValidationError):
            self.client.render("cursor", StdioServer("wrong-version", "fake"))
        with self.assertRaises(OutputLimitError):
            Client(binary_path=FAKE_BINARY, max_output_bytes=256).render(
                "cursor", StdioServer("overflow", "fake")
            )

    @unittest.skipUnless(os.name == "posix", "POSIX process-group behavior")
    def test_timeout_covers_request_writing_and_kills_process_group(self) -> None:
        started = time.monotonic()
        with mock.patch.dict(os.environ, {"DETECT_HARNESS_FAKE_MODE": "hang-tree"}):
            with self.assertRaises(InvocationError):
                Client(binary_path=FAKE_BINARY, timeout=0.05).render(
                    "cursor",
                    StdioServer("example", "fake", env={"LARGE": "x" * (512 * 1024)}),
                )
        self.assertLess(time.monotonic() - started, 2)

    @unittest.skipUnless(os.name == "posix", "POSIX process-group behavior")
    def test_output_limit_escalates_for_resistant_process_group(self) -> None:
        started = time.monotonic()
        with mock.patch.dict(os.environ, {"DETECT_HARNESS_FAKE_MODE": "overflow-tree"}):
            with self.assertRaises(OutputLimitError):
                Client(binary_path=FAKE_BINARY, max_output_bytes=256).detect()
        self.assertLess(time.monotonic() - started, 2)

    def test_project_scope_and_zoo_code(self) -> None:
        scope = project_scope("/tmp/project")
        detection = self.client.detect(scope)[0]
        self.assertEqual(detection.scope, "project")
        self.assertEqual(detection.scope_dir, "/tmp/project")

        changes = self.client.plan(
            ("zoo-code",), "present", self.server, scope=scope
        )
        self.assertEqual(changes[0].scope, "project")
        self.assertEqual(changes[0].scope_dir, "/tmp/project")
        self.assertEqual(changes[0].harness_id, "zoo-code")

        with self.assertRaises(ValueError):
            project_scope("  ")

        result = project_scope("/x")
        self.assertIsInstance(result, Scope)
        self.assertIsInstance(ProjectScope(path="p", reload_hint="r", lifecycle="l", shareable=True, trust_gate=False), ProjectScope)

    def test_roo_code_harness_is_accepted(self) -> None:
        changes = self.client.plan(("roo-code",), "present", self.server)
        self.assertEqual(changes[0].harness_id, "roo-code")

    def test_render_with_scope(self) -> None:
        scope = project_scope("/tmp/project")
        rendered = json.loads(self.client.render("cursor", self.server, scope))
        self.assertEqual(rendered["harness"], "cursor")


if __name__ == "__main__":
    unittest.main()
