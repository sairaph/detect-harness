//! Typed Rust client for the detect-harness protocol.

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::env;
use std::ffi::OsString;
use std::fmt;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, ExitStatus, Stdio};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

pub const PROTOCOL_VERSION: u32 = 1;
pub const DEFAULT_BINARY: &str = "detect-harness";
pub const DEFAULT_MAX_OUTPUT_BYTES: usize = 32 << 20;
const MAX_REQUEST_BYTES: usize = 1 << 20;

pub type Result<T> = std::result::Result<T, Error>;

#[derive(Clone, Copy, Debug, Eq, Hash, PartialEq, Serialize, Deserialize)]
pub enum HarnessId {
    #[serde(rename = "claude-desktop")]
    ClaudeDesktop,
    #[serde(rename = "claude-code")]
    ClaudeCode,
    #[serde(rename = "cursor")]
    Cursor,
    #[serde(rename = "codex")]
    Codex,
    #[serde(rename = "gemini-cli")]
    GeminiCli,
    #[serde(rename = "windsurf")]
    Windsurf,
    #[serde(rename = "zed")]
    Zed,
    #[serde(rename = "cline")]
    Cline,
    #[serde(rename = "roo-code")]
    RooCode,
    #[serde(rename = "amazon-q")]
    AmazonQ,
    #[serde(rename = "continue")]
    Continue,
    #[serde(rename = "opencode")]
    OpenCode,
    #[serde(rename = "vscode")]
    VsCode,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct StdioServer {
    pub name: String,
    pub command: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<String>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub env: BTreeMap<String, String>,
}

impl StdioServer {
    pub fn new(name: impl Into<String>, command: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            command: command.into(),
            args: Vec::new(),
            env: BTreeMap::new(),
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DetectionState {
    Present,
    Absent,
    Unavailable,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Detection {
    pub id: HarnessId,
    pub name: String,
    pub reload_hint: String,
    pub state: DetectionState,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub config_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub config_error: Option<String>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DesiredState {
    Present,
    Absent,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ConflictPolicy {
    Error,
    Replace,
}

impl Default for ConflictPolicy {
    fn default() -> Self {
        Self::Error
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ChangeState {
    Ready,
    Noop,
    Conflict,
    Unavailable,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ChangeAction {
    Add,
    Update,
    Remove,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Change {
    pub harness_id: HarnessId,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    pub desired: DesiredState,
    pub state: ChangeState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub action: Option<ChangeAction>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ResultState {
    Applied,
    Noop,
    Skipped,
    Conflict,
    Failed,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateResult {
    pub harness_id: HarnessId,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    pub desired: DesiredState,
    pub state: ResultState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub action: Option<ChangeAction>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct UpdateOutcome {
    pub changes: Vec<Change>,
    pub results: Vec<UpdateResult>,
}

#[derive(Clone, Debug, Eq, PartialEq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProtocolError {
    pub code: String,
    pub message: String,
}

impl fmt::Display for ProtocolError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(formatter, "{}: {}", self.code, self.message)
    }
}

impl std::error::Error for ProtocolError {}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum OutputStream {
    Stdout,
    Stderr,
}

impl fmt::Display for OutputStream {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Stdout => formatter.write_str("stdout"),
            Self::Stderr => formatter.write_str("stderr"),
        }
    }
}

#[derive(Debug)]
pub enum Error {
    EncodeRequest(serde_json::Error),
    RequestTooLarge {
        limit: usize,
    },
    Spawn {
        binary: PathBuf,
        source: io::Error,
    },
    WriteRequest(io::Error),
    RequestWriterPanicked,
    Wait(io::Error),
    Timeout {
        timeout: Duration,
    },
    ReadOutput {
        stream: OutputStream,
        source: io::Error,
    },
    OutputTooLarge {
        stream: OutputStream,
        limit: usize,
    },
    ReaderPanicked(OutputStream),
    DecodeResponse(serde_json::Error),
    UnsupportedProtocolVersion {
        expected: u32,
        actual: u32,
    },
    InvalidResponse(&'static str),
    Protocol(ProtocolError),
    ProcessFailed {
        code: Option<i32>,
        stderr: String,
    },
}

impl fmt::Display for Error {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::EncodeRequest(error) => write!(formatter, "failed to encode request: {error}"),
            Self::RequestTooLarge { limit } => {
                write!(formatter, "request exceeds the {limit}-byte limit")
            }
            Self::Spawn { binary, source } => {
                write!(formatter, "failed to start {}: {source}", binary.display())
            }
            Self::WriteRequest(error) => write!(formatter, "failed to write request: {error}"),
            Self::RequestWriterPanicked => formatter.write_str("companion request writer panicked"),
            Self::Wait(error) => write!(formatter, "failed to wait for companion binary: {error}"),
            Self::Timeout { timeout } => {
                write!(formatter, "companion timed out after {timeout:?}")
            }
            Self::ReadOutput { stream, source } => {
                write!(formatter, "failed to read companion {stream}: {source}")
            }
            Self::OutputTooLarge { stream, limit } => {
                write!(
                    formatter,
                    "companion {stream} exceeds the {limit}-byte limit"
                )
            }
            Self::ReaderPanicked(stream) => write!(formatter, "companion {stream} reader panicked"),
            Self::DecodeResponse(error) => write!(formatter, "invalid JSON response: {error}"),
            Self::UnsupportedProtocolVersion { expected, actual } => write!(
                formatter,
                "unsupported protocol version {actual}; expected {expected}"
            ),
            Self::InvalidResponse(message) => {
                write!(formatter, "invalid protocol response: {message}")
            }
            Self::Protocol(error) => write!(formatter, "companion error: {error}"),
            Self::ProcessFailed { code, stderr } => {
                write!(formatter, "companion process failed")?;
                if let Some(code) = code {
                    write!(formatter, " with exit code {code}")?;
                }
                if !stderr.trim().is_empty() {
                    write!(formatter, ": {}", stderr.trim())?;
                }
                Ok(())
            }
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::EncodeRequest(error) | Self::DecodeResponse(error) => Some(error),
            Self::Spawn { source, .. }
            | Self::WriteRequest(source)
            | Self::Wait(source)
            | Self::ReadOutput { source, .. } => Some(source),
            Self::Protocol(error) => Some(error),
            _ => None,
        }
    }
}

#[derive(Clone, Debug)]
pub struct Client {
    binary: Option<PathBuf>,
    max_output_bytes: usize,
    timeout: Option<Duration>,
}

impl Default for Client {
    fn default() -> Self {
        Self {
            binary: None,
            max_output_bytes: DEFAULT_MAX_OUTPUT_BYTES,
            timeout: None,
        }
    }
}

impl Client {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn with_binary(path: impl Into<PathBuf>) -> Self {
        Self {
            binary: Some(path.into()),
            ..Self::default()
        }
    }

    pub fn max_output_bytes(mut self, max_output_bytes: usize) -> Self {
        assert!(max_output_bytes > 0, "max_output_bytes must be positive");
        self.max_output_bytes = max_output_bytes;
        self
    }

    pub fn timeout(mut self, timeout: Duration) -> Self {
        assert!(!timeout.is_zero(), "timeout must be positive");
        self.timeout = Some(timeout);
        self
    }

    pub fn detect(&self) -> Result<Vec<Detection>> {
        let response = self.invoke(&Request::detect())?;
        response
            .detections
            .ok_or(Error::InvalidResponse("detect response omitted detections"))
    }

    pub fn render(&self, harness: HarnessId, server: &StdioServer) -> Result<String> {
        let response = self.invoke(&Request::render(harness, server))?;
        response
            .config
            .ok_or(Error::InvalidResponse("render response omitted config"))
    }

    pub fn plan(
        &self,
        harnesses: &[HarnessId],
        desired: DesiredState,
        server: &StdioServer,
    ) -> Result<Vec<Change>> {
        self.plan_with_conflict_policy(harnesses, desired, server, ConflictPolicy::Error)
    }

    pub fn plan_with_conflict_policy(
        &self,
        harnesses: &[HarnessId],
        desired: DesiredState,
        server: &StdioServer,
        conflict_policy: ConflictPolicy,
    ) -> Result<Vec<Change>> {
        let response = self.invoke(&Request::update(
            harnesses,
            desired,
            server,
            conflict_policy,
            true,
        ))?;
        response
            .changes
            .ok_or(Error::InvalidResponse("plan response omitted changes"))
    }

    pub fn update(
        &self,
        harnesses: &[HarnessId],
        desired: DesiredState,
        server: &StdioServer,
    ) -> Result<UpdateOutcome> {
        self.update_with_conflict_policy(harnesses, desired, server, ConflictPolicy::Error)
    }

    pub fn update_with_conflict_policy(
        &self,
        harnesses: &[HarnessId],
        desired: DesiredState,
        server: &StdioServer,
        conflict_policy: ConflictPolicy,
    ) -> Result<UpdateOutcome> {
        let response = self.invoke(&Request::update(
            harnesses,
            desired,
            server,
            conflict_policy,
            false,
        ))?;
        Ok(UpdateOutcome {
            changes: response
                .changes
                .ok_or(Error::InvalidResponse("update response omitted changes"))?,
            results: response
                .results
                .ok_or(Error::InvalidResponse("update response omitted results"))?,
        })
    }

    fn invoke(&self, request: &Request<'_>) -> Result<Response> {
        let encoded = serde_json::to_vec(request).map_err(Error::EncodeRequest)?;
        if encoded.len() > MAX_REQUEST_BYTES {
            return Err(Error::RequestTooLarge {
                limit: MAX_REQUEST_BYTES,
            });
        }

        let binary = select_binary(self.binary.as_deref(), env::var_os("DETECT_HARNESS_BIN"));
        let mut child = Command::new(&binary)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|source| Error::Spawn {
                binary: PathBuf::from(binary.clone()),
                source,
            })?;

        let stdin = child
            .stdin
            .take()
            .ok_or(Error::InvalidResponse("companion stdin was not piped"))?;
        let stdout = child
            .stdout
            .take()
            .ok_or(Error::InvalidResponse("companion stdout was not piped"))?;
        let stderr = child
            .stderr
            .take()
            .ok_or(Error::InvalidResponse("companion stderr was not piped"))?;

        let reader_failed = Arc::new(AtomicBool::new(false));
        let output_bytes = Arc::new(AtomicUsize::new(0));
        let stdout_reader = spawn_reader(
            stdout,
            self.max_output_bytes,
            Arc::clone(&output_bytes),
            Arc::clone(&reader_failed),
        );
        let stderr_reader = spawn_reader(
            stderr,
            self.max_output_bytes,
            Arc::clone(&output_bytes),
            Arc::clone(&reader_failed),
        );

        let request_writer = thread::spawn(move || {
            let mut stdin = stdin;
            stdin.write_all(&encoded).and_then(|_| stdin.flush())
        });

        let status = wait_for_child(&mut child, &reader_failed, self.timeout)?;
        let stdout = join_reader(stdout_reader, OutputStream::Stdout, self.max_output_bytes)?;
        let stderr = join_reader(stderr_reader, OutputStream::Stderr, self.max_output_bytes)?;
        request_writer
            .join()
            .map_err(|_| Error::RequestWriterPanicked)?
            .map_err(Error::WriteRequest)?;

        parse_response(status, &stdout, &stderr)
    }
}

fn select_binary(explicit: Option<&Path>, configured: Option<OsString>) -> OsString {
    if let Some(path) = explicit {
        return path.as_os_str().to_owned();
    }
    if let Some(path) = configured.filter(|path| !path.is_empty()) {
        return path;
    }
    OsString::from(DEFAULT_BINARY)
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Request<'a> {
    version: u32,
    operation: Operation,
    #[serde(skip_serializing_if = "Option::is_none")]
    server: Option<&'a StdioServer>,
    #[serde(skip_serializing_if = "Option::is_none")]
    harness: Option<HarnessId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    harnesses: Option<&'a [HarnessId]>,
    #[serde(skip_serializing_if = "Option::is_none")]
    desired: Option<DesiredState>,
    #[serde(skip_serializing_if = "Option::is_none")]
    conflict_policy: Option<ConflictPolicy>,
    #[serde(skip_serializing_if = "Option::is_none")]
    dry_run: Option<bool>,
}

impl<'a> Request<'a> {
    fn detect() -> Self {
        Self {
            version: PROTOCOL_VERSION,
            operation: Operation::Detect,
            server: None,
            harness: None,
            harnesses: None,
            desired: None,
            conflict_policy: None,
            dry_run: None,
        }
    }

    fn render(harness: HarnessId, server: &'a StdioServer) -> Self {
        Self {
            version: PROTOCOL_VERSION,
            operation: Operation::Render,
            server: Some(server),
            harness: Some(harness),
            harnesses: None,
            desired: None,
            conflict_policy: None,
            dry_run: None,
        }
    }

    fn update(
        harnesses: &'a [HarnessId],
        desired: DesiredState,
        server: &'a StdioServer,
        conflict_policy: ConflictPolicy,
        dry_run: bool,
    ) -> Self {
        Self {
            version: PROTOCOL_VERSION,
            operation: Operation::Update,
            server: Some(server),
            harness: None,
            harnesses: Some(harnesses),
            desired: Some(desired),
            conflict_policy: Some(conflict_policy),
            dry_run: Some(dry_run),
        }
    }
}

#[derive(Clone, Copy, Serialize)]
#[serde(rename_all = "lowercase")]
enum Operation {
    Detect,
    Render,
    Update,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Response {
    #[serde(rename = "version")]
    _version: u32,
    ok: bool,
    #[serde(default)]
    detections: Option<Vec<Detection>>,
    #[serde(default)]
    config: Option<String>,
    #[serde(default)]
    changes: Option<Vec<Change>>,
    #[serde(default)]
    results: Option<Vec<UpdateResult>>,
    #[serde(default)]
    error: Option<ProtocolError>,
}

#[derive(Deserialize)]
struct VersionEnvelope {
    version: u32,
}

enum ReadFailure {
    Io(io::Error),
    TooLarge,
}

fn spawn_reader<R>(
    reader: R,
    limit: usize,
    output_bytes: Arc<AtomicUsize>,
    reader_failed: Arc<AtomicBool>,
) -> thread::JoinHandle<std::result::Result<Vec<u8>, ReadFailure>>
where
    R: Read + Send + 'static,
{
    thread::spawn(move || {
        let result = read_bounded(reader, limit, &output_bytes);
        if result.is_err() {
            reader_failed.store(true, Ordering::Release);
        }
        result
    })
}

fn read_bounded(
    mut reader: impl Read,
    limit: usize,
    output_bytes: &AtomicUsize,
) -> std::result::Result<Vec<u8>, ReadFailure> {
    let mut output = Vec::with_capacity(limit.min(8192));
    let mut buffer = [0_u8; 8192];
    loop {
        let count = reader.read(&mut buffer).map_err(ReadFailure::Io)?;
        if count == 0 {
            return Ok(output);
        }
        let previous = output_bytes.fetch_add(count, Ordering::AcqRel);
        if count > limit || previous > limit - count {
            return Err(ReadFailure::TooLarge);
        }
        output.extend_from_slice(&buffer[..count]);
    }
}

fn wait_for_child(
    child: &mut std::process::Child,
    reader_failed: &AtomicBool,
    timeout: Option<Duration>,
) -> Result<ExitStatus> {
    let started = Instant::now();
    loop {
        if reader_failed.load(Ordering::Acquire) {
            let _ = child.kill();
            return child.wait().map_err(Error::Wait);
        }
        if let Some(status) = child.try_wait().map_err(Error::Wait)? {
            return Ok(status);
        }
        if let Some(timeout) = timeout {
            if started.elapsed() >= timeout {
                let _ = child.kill();
                child.wait().map_err(Error::Wait)?;
                return Err(Error::Timeout { timeout });
            }
        }
        thread::sleep(Duration::from_millis(10));
    }
}

fn join_reader(
    reader: thread::JoinHandle<std::result::Result<Vec<u8>, ReadFailure>>,
    stream: OutputStream,
    limit: usize,
) -> Result<Vec<u8>> {
    match reader.join().map_err(|_| Error::ReaderPanicked(stream))? {
        Ok(output) => Ok(output),
        Err(ReadFailure::Io(source)) => Err(Error::ReadOutput { stream, source }),
        Err(ReadFailure::TooLarge) => Err(Error::OutputTooLarge { stream, limit }),
    }
}

fn parse_response(status: ExitStatus, stdout: &[u8], stderr: &[u8]) -> Result<Response> {
    let envelope: VersionEnvelope = match serde_json::from_slice(stdout) {
        Ok(envelope) => envelope,
        Err(_) if !status.success() => {
            return Err(Error::ProcessFailed {
                code: status.code(),
                stderr: String::from_utf8_lossy(stderr).into_owned(),
            });
        }
        Err(source) => return Err(Error::DecodeResponse(source)),
    };
    if envelope.version != PROTOCOL_VERSION {
        return Err(Error::UnsupportedProtocolVersion {
            expected: PROTOCOL_VERSION,
            actual: envelope.version,
        });
    }
    let response: Response = serde_json::from_slice(stdout).map_err(Error::DecodeResponse)?;
    if !response.ok {
        return match response.error {
            Some(error) => Err(Error::Protocol(error)),
            None => Err(Error::InvalidResponse("failed response omitted error")),
        };
    }
    if response.error.is_some() {
        return Err(Error::InvalidResponse("successful response included error"));
    }
    if !status.success() {
        return Err(Error::ProcessFailed {
            code: status.code(),
            stderr: String::from_utf8_lossy(stderr).into_owned(),
        });
    }
    Ok(response)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn binary_precedence_is_explicit_then_environment_then_path() {
        assert_eq!(
            select_binary(
                Some(Path::new("/explicit/bin")),
                Some(OsString::from("env-bin"))
            ),
            OsString::from("/explicit/bin")
        );
        assert_eq!(
            select_binary(None, Some(OsString::from("env-bin"))),
            OsString::from("env-bin")
        );
        assert_eq!(
            select_binary(None, Some(OsString::new())),
            OsString::from(DEFAULT_BINARY)
        );
        assert_eq!(select_binary(None, None), OsString::from(DEFAULT_BINARY));
    }

    #[test]
    fn update_request_marks_plans_as_dry_runs() {
        let server = StdioServer::new("example", "example-server");
        let harnesses = [HarnessId::Codex];
        let request = Request::update(
            &harnesses,
            DesiredState::Present,
            &server,
            ConflictPolicy::Error,
            true,
        );
        let value = serde_json::to_value(request).unwrap();
        assert_eq!(value["version"], PROTOCOL_VERSION);
        assert_eq!(value["operation"], "update");
        assert_eq!(value["harnesses"][0], "codex");
        assert_eq!(value["conflictPolicy"], "error");
        assert_eq!(value["dryRun"], true);
    }
}
