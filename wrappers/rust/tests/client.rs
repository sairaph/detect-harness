#![cfg(unix)]

use detect_harness::{
    ChangeState, Client, ConflictPolicy, DesiredState, DetectionState, Error, HarnessId,
    OutputStream, ResultState, StdioServer,
};
use serde_json::Value;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};

static NEXT_FAKE: AtomicUsize = AtomicUsize::new(0);

struct FakeBinary {
    directory: PathBuf,
    executable: PathBuf,
    request: PathBuf,
}

impl FakeBinary {
    fn new(response: &str, exit_code: i32) -> Self {
        let id = NEXT_FAKE.fetch_add(1, Ordering::Relaxed);
        let directory =
            std::env::temp_dir().join(format!("detect-harness-rust-{}-{id}", std::process::id()));
        fs::create_dir_all(&directory).unwrap();
        let executable = directory.join("fake-detect-harness");
        let temporary = directory.join("fake-detect-harness.tmp");
        let request = directory.join("request.json");
        let script = format!(
            "#!/bin/sh\n[ \"$#\" -eq 0 ] || exit 97\nIFS= read -r request\nprintf '%s' \"$request\" > {}\nprintf '%s\\n' {}\nexit {}\n",
            shell_quote(&request),
            shell_quote(Path::new(response)),
            exit_code
        );
        fs::write(&temporary, script).unwrap();
        let mut permissions = fs::metadata(&temporary).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&temporary, permissions).unwrap();
        fs::rename(temporary, &executable).unwrap();
        Self {
            directory,
            executable,
            request,
        }
    }

    fn client(&self) -> Client {
        Client::with_binary(&self.executable)
    }

    fn request(&self) -> Value {
        serde_json::from_slice(&fs::read(&self.request).unwrap()).unwrap()
    }
}

impl Drop for FakeBinary {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

fn shell_quote(value: &Path) -> String {
    format!("'{}'", value.to_string_lossy().replace('\'', "'\\''"))
}

fn server() -> StdioServer {
    let mut server = StdioServer::new("example", "example-server");
    server.args.push("--stdio".into());
    server.env.insert("TOKEN".into(), "secret".into());
    server
}

#[test]
fn detect_is_typed_and_uses_stdin_without_arguments() {
    let fake = FakeBinary::new(
        r#"{"version":1,"ok":true,"detections":[{"id":"codex","name":"Codex","reloadHint":"Restart Codex","state":"present","evidence":["/bin/codex"]}]}"#,
        0,
    );

    let detections = fake.client().detect().unwrap();

    assert_eq!(detections[0].id, HarnessId::Codex);
    assert_eq!(detections[0].state, DetectionState::Present);
    assert_eq!(fake.request()["operation"], "detect");
    assert_eq!(fake.request().as_object().unwrap().len(), 2);
}

#[test]
fn render_plan_and_update_use_the_expected_protocol_shapes() {
    let rendered = FakeBinary::new(
        r#"{"version":1,"ok":true,"config":"{\n  \"mcpServers\": {}\n}\n"}"#,
        0,
    );
    let config = rendered
        .client()
        .render(HarnessId::ClaudeCode, &server())
        .unwrap();
    assert!(config.contains("mcpServers"));
    let request = rendered.request();
    assert_eq!(request["operation"], "render");
    assert_eq!(request["harness"], "claude-code");
    assert_eq!(request["server"]["env"]["TOKEN"], "secret");

    let planned = FakeBinary::new(
        r#"{"version":1,"ok":true,"changes":[{"harnessId":"codex","name":"Codex","desired":"present","state":"ready","action":"add"}]}"#,
        0,
    );
    let changes = planned
        .client()
        .plan_with_conflict_policy(
            &[HarnessId::Codex],
            DesiredState::Present,
            &server(),
            ConflictPolicy::Replace,
        )
        .unwrap();
    assert_eq!(changes[0].state, ChangeState::Ready);
    assert_eq!(planned.request()["dryRun"], true);

    let updated = FakeBinary::new(
        r#"{"version":1,"ok":true,"changes":[{"harnessId":"codex","name":"Codex","desired":"absent","state":"ready","action":"remove"}],"results":[{"harnessId":"codex","name":"Codex","desired":"absent","state":"applied","action":"remove"}]}"#,
        0,
    );
    let outcome = updated
        .client()
        .update(&[HarnessId::Codex], DesiredState::Absent, &server())
        .unwrap();
    assert_eq!(outcome.results[0].state, ResultState::Applied);
    assert_eq!(updated.request()["dryRun"], false);
    assert_eq!(updated.request()["conflictPolicy"], "error");
}

#[test]
fn structured_errors_and_protocol_versions_are_checked() {
    let failed = FakeBinary::new(
        r#"{"version":1,"ok":false,"error":{"code":"invalid_request","message":"bad input"}}"#,
        2,
    );
    match failed.client().detect().unwrap_err() {
        Error::Protocol(error) => {
            assert_eq!(error.code, "invalid_request");
            assert_eq!(error.message, "bad input");
        }
        error => panic!("unexpected error: {error}"),
    }

    let wrong_version = FakeBinary::new(
        r#"{"version":2,"ok":true,"detections":[],"futureField":true}"#,
        0,
    );
    assert!(matches!(
        wrong_version.client().detect().unwrap_err(),
        Error::UnsupportedProtocolVersion {
            expected: 1,
            actual: 2
        }
    ));
}

#[test]
fn output_is_bounded() {
    let fake = FakeBinary::new(r#"{"version":1,"ok":true,"detections":[]}"#, 0);
    let error = fake.client().max_output_bytes(8).detect().unwrap_err();
    assert!(matches!(
        error,
        Error::OutputTooLarge {
            stream: OutputStream::Stdout,
            limit: 8
        }
    ));
}
