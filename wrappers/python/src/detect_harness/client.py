from __future__ import annotations

import json
import math
import os
import signal
import subprocess
import threading
import time
from dataclasses import dataclass, field
from typing import Any, BinaryIO, Literal, Mapping, Sequence, TypeAlias, TypedDict, cast

PROTOCOL_VERSION = 1
DEFAULT_MAX_OUTPUT_BYTES = 32 * 1024 * 1024
_TERMINATION_GRACE_SECONDS = 0.25

HarnessId: TypeAlias = Literal[
    "claude-desktop",
    "claude-code",
    "cursor",
    "codex",
    "gemini-cli",
    "windsurf",
    "zed",
    "cline",
    "roo-code",
    "zoo-code",
    "amazon-q",
    "continue",
    "opencode",
    "vscode",
]
DesiredState: TypeAlias = Literal["present", "absent"]
ConflictPolicy: TypeAlias = Literal["error", "replace"]
DetectionState: TypeAlias = Literal["present", "absent", "unavailable"]
ChangeState: TypeAlias = Literal["ready", "noop", "conflict", "unavailable"]
ResultState: TypeAlias = Literal["applied", "noop", "skipped", "conflict", "failed"]
ChangeAction: TypeAlias = Literal["add", "update", "remove"]
ScopeMode: TypeAlias = Literal["project"]
_HARNESS_IDS = (
    "claude-desktop",
    "claude-code",
    "cursor",
    "codex",
    "gemini-cli",
    "windsurf",
    "zed",
    "cline",
    "roo-code",
    "zoo-code",
    "amazon-q",
    "continue",
    "opencode",
    "vscode",
)


class _InvocationDetails(TypedDict):
    exit_code: int | None
    signal: int | None
    stderr: str


@dataclass(frozen=True, slots=True)
class StdioServer:
    name: str
    command: str
    args: tuple[str, ...] = ()
    env: Mapping[str, str] = field(default_factory=dict)

    def _protocol_value(self) -> dict[str, object]:
        value: dict[str, object] = {"name": self.name, "command": self.command}
        if self.args:
            value["args"] = list(self.args)
        if self.env:
            value["env"] = dict(self.env)
        return value


@dataclass(frozen=True, slots=True)
class Scope:
    """Selects directory-local (project) configuration for supported harnesses."""

    mode: ScopeMode
    dir: str

    def _protocol_value(self) -> dict[str, object]:
        return {"mode": self.mode, "dir": self.dir}


def project_scope(directory: str) -> Scope:
    """Build a project scope targeting directory. Raises ValueError if empty."""
    if not isinstance(directory, str) or not directory.strip():
        raise ValueError("project scope requires a directory")
    return Scope(mode="project", dir=directory.strip())


@dataclass(frozen=True, slots=True)
class ProjectScope:
    """Project-scoped configuration support metadata. Informational."""

    path: str
    reload_hint: str
    lifecycle: str
    shareable: bool
    trust_gate: bool


@dataclass(frozen=True, slots=True)
class Detection:
    id: HarnessId
    name: str
    reload_hint: str
    state: DetectionState
    evidence: tuple[str, ...] = ()
    reason: str | None = None
    config_path: str | None = None
    config_error: str | None = None
    scope: ScopeMode | None = None
    scope_dir: str | None = None
    project: ProjectScope | None = None


@dataclass(frozen=True, slots=True)
class Change:
    harness_id: HarnessId
    name: str
    desired: DesiredState
    state: ChangeState
    path: str | None = None
    action: ChangeAction | None = None
    reason: str | None = None
    scope: ScopeMode | None = None
    scope_dir: str | None = None


@dataclass(frozen=True, slots=True)
class UpdateResult:
    harness_id: HarnessId
    name: str
    desired: DesiredState
    state: ResultState
    path: str | None = None
    action: ChangeAction | None = None
    reason: str | None = None
    scope: ScopeMode | None = None
    scope_dir: str | None = None


@dataclass(frozen=True, slots=True)
class UpdateOutcome:
    changes: tuple[Change, ...]
    results: tuple[UpdateResult, ...]


class DetectHarnessError(Exception):
    """Base class for companion invocation and protocol failures."""


class InvocationError(DetectHarnessError):
    def __init__(
        self,
        message: str,
        *,
        exit_code: int | None = None,
        signal: int | None = None,
        stderr: str = "",
    ) -> None:
        super().__init__(message)
        self.exit_code = exit_code
        self.signal = signal
        self.stderr = stderr


class OutputLimitError(InvocationError):
    pass


class ProtocolValidationError(InvocationError):
    pass


class ProtocolError(InvocationError):
    def __init__(
        self,
        code: str,
        message: str,
        *,
        exit_code: int | None = None,
        signal: int | None = None,
        stderr: str = "",
    ) -> None:
        super().__init__(
            f"{code}: {message}", exit_code=exit_code, signal=signal, stderr=stderr
        )
        self.code = code


class Client:
    def __init__(
        self,
        *,
        binary_path: str | os.PathLike[str] | None = None,
        max_output_bytes: int = DEFAULT_MAX_OUTPUT_BYTES,
        timeout: float | None = None,
    ) -> None:
        if binary_path is not None and not os.fspath(binary_path):
            raise ValueError("binary_path cannot be empty")
        if (
            not isinstance(max_output_bytes, int)
            or isinstance(max_output_bytes, bool)
            or max_output_bytes <= 0
        ):
            raise ValueError("max_output_bytes must be a positive integer")
        if timeout is not None and (not math.isfinite(timeout) or timeout <= 0):
            raise ValueError("timeout must be positive")
        self._binary_path = os.fspath(binary_path) if binary_path is not None else None
        self._max_output_bytes = max_output_bytes
        self._timeout = timeout

    def detect(self, scope: Scope | None = None) -> tuple[Detection, ...]:
        request: dict[str, object] = {"version": PROTOCOL_VERSION, "operation": "detect"}
        _apply_scope(request, scope)
        response = self._invoke(request)
        return _parse_detections(response.get("detections"))

    def render(
        self, harness: HarnessId, server: StdioServer, scope: Scope | None = None
    ) -> str:
        request: dict[str, object] = {
            "version": PROTOCOL_VERSION,
            "operation": "render",
            "harness": harness,
            "server": server._protocol_value(),
        }
        _apply_scope(request, scope)
        response = self._invoke(request)
        return _expect_string(response.get("config"), "response.config")

    def plan(
        self,
        harnesses: Sequence[HarnessId],
        desired: DesiredState,
        server: StdioServer,
        *,
        conflict_policy: ConflictPolicy = "error",
        scope: Scope | None = None,
    ) -> tuple[Change, ...]:
        request = self._update_request(harnesses, desired, server, conflict_policy, scope)
        request["dryRun"] = True
        response = self._invoke(request)
        return _parse_changes(response.get("changes"))

    def update(
        self,
        harnesses: Sequence[HarnessId],
        desired: DesiredState,
        server: StdioServer,
        *,
        conflict_policy: ConflictPolicy = "error",
        scope: Scope | None = None,
    ) -> UpdateOutcome:
        response = self._invoke(
            self._update_request(harnesses, desired, server, conflict_policy, scope)
        )
        return UpdateOutcome(
            changes=_parse_changes(response.get("changes")),
            results=_parse_results(response.get("results")),
        )

    @staticmethod
    def _update_request(
        harnesses: Sequence[HarnessId],
        desired: DesiredState,
        server: StdioServer,
        conflict_policy: ConflictPolicy,
        scope: Scope | None,
    ) -> dict[str, object]:
        request: dict[str, object] = {
            "version": PROTOCOL_VERSION,
            "operation": "update",
            "harnesses": list(harnesses),
            "desired": desired,
            "server": server._protocol_value(),
        }
        request["conflictPolicy"] = conflict_policy
        _apply_scope(request, scope)
        return request

    def _invoke(self, request: Mapping[str, object]) -> dict[str, Any]:
        binary = self._binary_path or os.environ.get("DETECT_HARNESS_BIN") or "detect-harness"
        payload = json.dumps(request, separators=(",", ":")).encode("utf-8")
        try:
            process = subprocess.Popen(
                [binary],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                shell=False,
                start_new_session=os.name == "posix",
            )
        except OSError as error:
            raise InvocationError(f"could not execute {binary!r}: {error}") from error

        if process.stdin is None:
            raise InvocationError("companion stdin was not piped")
        if process.stdout is None:
            raise InvocationError("companion stdout was not piped")
        if process.stderr is None:
            raise InvocationError("companion stderr was not piped")
        stdout: list[bytes] = []
        stderr: list[bytes] = []
        lock = threading.Lock()
        collected_bytes = 0
        output_exceeded = threading.Event()
        termination_lock = threading.Lock()
        termination_started = False
        kill_timer: threading.Timer | None = None

        def signal_process_tree(*, force: bool) -> None:
            try:
                if os.name == "posix":
                    os.killpg(process.pid, signal.SIGKILL if force else signal.SIGTERM)
                elif force:
                    process.kill()
                else:
                    process.terminate()
            except OSError:
                pass

        def terminate() -> None:
            nonlocal termination_started, kill_timer
            with termination_lock:
                if termination_started:
                    return
                termination_started = True
            signal_process_tree(force=False)
            kill_timer = threading.Timer(
                _TERMINATION_GRACE_SECONDS, signal_process_tree, kwargs={"force": True}
            )
            kill_timer.daemon = True
            kill_timer.start()

        def collect(stream: BinaryIO, destination: list[bytes]) -> None:
            nonlocal collected_bytes
            while chunk := stream.read1(64 * 1024):
                with lock:
                    if output_exceeded.is_set():
                        break
                    collected_bytes += len(chunk)
                    if collected_bytes > self._max_output_bytes:
                        output_exceeded.set()
                    else:
                        destination.append(chunk)
                if output_exceeded.is_set():
                    terminate()
                    break

        readers = (
            threading.Thread(target=collect, args=(process.stdout, stdout), daemon=True),
            threading.Thread(target=collect, args=(process.stderr, stderr), daemon=True),
        )
        for reader in readers:
            reader.start()

        def write_request() -> None:
            try:
                process.stdin.write(payload)
                process.stdin.flush()
            except (BrokenPipeError, OSError):
                pass
            finally:
                try:
                    process.stdin.close()
                except OSError:
                    pass

        writer = threading.Thread(target=write_request, daemon=True)
        deadline = None if self._timeout is None else time.monotonic() + self._timeout
        writer.start()

        timed_out = False
        try:
            remaining = None if deadline is None else max(0.0, deadline - time.monotonic())
            return_code = process.wait(timeout=remaining)
        except subprocess.TimeoutExpired:
            timed_out = True
            terminate()
            return_code = process.wait()
        writer.join()
        for reader in readers:
            reader.join()
        if kill_timer is not None:
            kill_timer.cancel()
        process.stdout.close()
        process.stderr.close()

        stdout_text = b"".join(stdout).decode("utf-8", errors="replace")
        stderr_text = b"".join(stderr).decode("utf-8", errors="replace")
        exit_code = return_code if return_code >= 0 else None
        exit_signal = -return_code if return_code < 0 else None
        details: _InvocationDetails = {
            "exit_code": exit_code,
            "signal": exit_signal,
            "stderr": stderr_text,
        }

        if output_exceeded.is_set():
            raise OutputLimitError(
                f"companion output exceeded {self._max_output_bytes} bytes", **details
            )
        if timed_out:
            raise InvocationError(f"companion timed out after {self._timeout} seconds", **details)

        try:
            decoded = json.loads(stdout_text)
        except (json.JSONDecodeError, UnicodeError) as error:
            raise ProtocolValidationError(
                f"companion returned invalid JSON{_diagnostic_suffix(stderr_text)}", **details
            ) from error

        response = _parse_envelope(
            decoded, exit_code=exit_code, signal=exit_signal, stderr=stderr_text
        )
        if return_code != 0:
            status: int | str = exit_code if exit_code is not None else f"signal {exit_signal}"
            raise InvocationError(
                f"companion returned a successful response but exited with status {status}"
                f"{_diagnostic_suffix(stderr_text)}",
                **details,
            )
        return response


def _parse_envelope(
    value: object,
    *,
    exit_code: int | None,
    signal: int | None,
    stderr: str,
) -> dict[str, Any]:
    response = _expect_record(value, "response")
    if response.get("version") != PROTOCOL_VERSION:
        raise ProtocolValidationError(
            f"unsupported protocol version {response.get('version')!r}; expected {PROTOCOL_VERSION}",
            exit_code=exit_code,
            signal=signal,
            stderr=stderr,
        )
    if not isinstance(response.get("ok"), bool):
        raise ProtocolValidationError(
            "response.ok must be a boolean",
            exit_code=exit_code,
            signal=signal,
            stderr=stderr,
        )
    if not response["ok"]:
        error = _expect_record(response.get("error"), "response.error")
        raise ProtocolError(
            _expect_string(error.get("code"), "response.error.code"),
            _expect_string(error.get("message"), "response.error.message"),
            exit_code=exit_code,
            signal=signal,
            stderr=stderr,
        )
    return response


def _parse_detections(value: object) -> tuple[Detection, ...]:
    detections: list[Detection] = []
    for index, entry in enumerate(_expect_list(value, "response.detections")):
        path = f"response.detections[{index}]"
        item = _expect_record(entry, path)
        detections.append(
            Detection(
                id=cast(HarnessId, _expect_enum(item.get("id"), _HARNESS_IDS, f"{path}.id")),
                name=_expect_string(item.get("name"), f"{path}.name"),
                reload_hint=_expect_string(item.get("reloadHint"), f"{path}.reloadHint"),
                state=cast(DetectionState, _expect_enum(item.get("state"), ("present", "absent", "unavailable"), f"{path}.state")),
                evidence=tuple(_expect_string_list(item.get("evidence", []), f"{path}.evidence")),
                reason=_optional_member_string(item, "reason", path),
                config_path=_optional_member_string(item, "configPath", path),
                config_error=_optional_member_string(item, "configError", path),
                scope=cast(
                    ScopeMode | None,
                    _optional_member_enum(item, "scope", ("project",), path),
                ),
                scope_dir=_optional_member_string(item, "scopeDir", path),
                project=_optional_project(item, path),
            )
        )
    return tuple(detections)


def _parse_changes(value: object) -> tuple[Change, ...]:
    changes: list[Change] = []
    for index, entry in enumerate(_expect_list(value, "response.changes")):
        path = f"response.changes[{index}]"
        item = _expect_record(entry, path)
        changes.append(
            Change(
                harness_id=cast(HarnessId, _expect_enum(item.get("harnessId"), _HARNESS_IDS, f"{path}.harnessId")),
                name=_expect_string(item.get("name"), f"{path}.name"),
                desired=cast(DesiredState, _expect_enum(item.get("desired"), ("present", "absent"), f"{path}.desired")),
                state=cast(ChangeState, _expect_enum(item.get("state"), ("ready", "noop", "conflict", "unavailable"), f"{path}.state")),
                path=_optional_member_string(item, "path", path),
                action=cast(
                    ChangeAction | None,
                    _optional_member_enum(
                        item, "action", ("add", "update", "remove"), path
                    ),
                ),
                reason=_optional_member_string(item, "reason", path),
                scope=cast(
                    ScopeMode | None,
                    _optional_member_enum(item, "scope", ("project",), path),
                ),
                scope_dir=_optional_member_string(item, "scopeDir", path),
            )
        )
    return tuple(changes)


def _parse_results(value: object) -> tuple[UpdateResult, ...]:
    results: list[UpdateResult] = []
    for index, entry in enumerate(_expect_list(value, "response.results")):
        path = f"response.results[{index}]"
        item = _expect_record(entry, path)
        results.append(
            UpdateResult(
                harness_id=cast(HarnessId, _expect_enum(item.get("harnessId"), _HARNESS_IDS, f"{path}.harnessId")),
                name=_expect_string(item.get("name"), f"{path}.name"),
                desired=cast(DesiredState, _expect_enum(item.get("desired"), ("present", "absent"), f"{path}.desired")),
                state=cast(ResultState, _expect_enum(item.get("state"), ("applied", "noop", "skipped", "conflict", "failed"), f"{path}.state")),
                path=_optional_member_string(item, "path", path),
                action=cast(
                    ChangeAction | None,
                    _optional_member_enum(
                        item, "action", ("add", "update", "remove"), path
                    ),
                ),
                reason=_optional_member_string(item, "reason", path),
                scope=cast(
                    ScopeMode | None,
                    _optional_member_enum(item, "scope", ("project",), path),
                ),
                scope_dir=_optional_member_string(item, "scopeDir", path),
            )
        )
    return tuple(results)


def _apply_scope(request: dict[str, object], scope: Scope | None) -> None:
    if scope is None:
        return
    if scope.mode != "project" or not str(scope.dir).strip():
        raise ValueError("project scope requires a directory")
    request["scope"] = scope._protocol_value()


def _optional_project(item: Mapping[str, object], path: str) -> ProjectScope | None:
    if "project" not in item:
        return None
    raw = item["project"]
    field_path = f"{path}.project"
    record = _expect_record(raw, field_path)
    return ProjectScope(
        path=_expect_string(record.get("path"), f"{field_path}.path"),
        reload_hint=_expect_string(record.get("reloadHint"), f"{field_path}.reloadHint"),
        lifecycle=_expect_string(record.get("lifecycle"), f"{field_path}.lifecycle"),
        shareable=_expect_bool(record.get("shareable"), f"{field_path}.shareable"),
        trust_gate=_expect_bool(record.get("trustGate"), f"{field_path}.trustGate"),
    )


def _expect_record(value: object, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ProtocolValidationError(f"{path} must be an object")
    return value


def _expect_list(value: object, path: str) -> list[object]:
    if not isinstance(value, list):
        raise ProtocolValidationError(f"{path} must be an array")
    return value


def _expect_string(value: object, path: str) -> str:
    if not isinstance(value, str):
        raise ProtocolValidationError(f"{path} must be a string")
    return value


def _expect_bool(value: object, path: str) -> bool:
    if not isinstance(value, bool):
        raise ProtocolValidationError(f"{path} must be a boolean")
    return value


def _optional_member_string(
    item: Mapping[str, object], key: str, path: str
) -> str | None:
    return None if key not in item else _expect_string(item[key], f"{path}.{key}")


def _optional_member_enum(
    item: Mapping[str, object], key: str, allowed: tuple[str, ...], path: str
) -> str | None:
    return None if key not in item else _expect_enum(item[key], allowed, f"{path}.{key}")


def _expect_string_list(value: object, path: str) -> list[str]:
    return [_expect_string(entry, f"{path}[{index}]") for index, entry in enumerate(_expect_list(value, path))]


def _expect_enum(value: object, allowed: tuple[str, ...], path: str) -> str:
    if not isinstance(value, str) or value not in allowed:
        raise ProtocolValidationError(f"{path} must be one of {', '.join(allowed)}")
    return value


def _diagnostic_suffix(stderr: str) -> str:
    diagnostic = stderr.strip()
    return "" if not diagnostic else f": {diagnostic}"
