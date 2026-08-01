//! Odo desktop app: thin Tauri 2 shell over the Go daemon.
//!
//! Responsibilities (and their only responsibilities):
//!   1. Daemon lifecycle — spawn the `odo` daemon on app start if its socket
//!      is not already answering, rebuilding the binary if missing.
//!   2. Unix-socket client — one JSON line out, one JSON line back over
//!      `<project>/.odo/odo.sock`.
//!   3. Tauri commands — expose the daemon's IPC verbs to the frontend.
//!
//! This layer holds no application state and no business logic: the daemon
//! (SQLite journal) is the single source of truth (Invariant 1, ADR-0001,
//! ADR-0002).

use serde_json::{json, Value};
use std::fs::OpenOptions;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::Duration;

/// Generous ceiling for one round trip. `poll_events` can run `git diff` on a
/// large worktree and `accept_diff` runs `git apply`; both are slowest on the
/// first call while the OS warms caches.
const READ_TIMEOUT: Duration = Duration::from_secs(120);

/// How long to wait for a freshly spawned daemon to answer its socket.
const STARTUP_TIMEOUT: Duration = Duration::from_secs(15);

/// Dev mode: the project IS this repository — `gui/src-tauri` up two levels.
fn default_project_root() -> Result<String, String> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let root = root
        .canonicalize()
        .map_err(|e| format!("resolve repo root {}: {e}", root.display()))?;
    Ok(root.to_string_lossy().into_owned())
}

fn resolve_root(project_root: Option<String>) -> Result<String, String> {
    match project_root {
        Some(r) if !r.is_empty() => Ok(r),
        _ => default_project_root(),
    }
}

fn socket_path(project_root: &str) -> PathBuf {
    Path::new(project_root).join(".odo").join("odo.sock")
}

fn daemon_binary(project_root: &str) -> PathBuf {
    Path::new(project_root).join("odo")
}

/// Single request → single response on a fresh connection. The daemon serves
/// one connection at a time until EOF, so keeping connections open would
/// starve the polling loop; every call connects, exchanges, and drops.
fn round_trip(project_root: &str, req: &Value) -> Result<Value, String> {
    let socket = socket_path(project_root);
    let mut stream = UnixStream::connect(&socket).map_err(|e| format!("connect {}: {e}", socket.display()))?;
    let _ = stream.set_read_timeout(Some(READ_TIMEOUT));

    stream
        .write_all(req.to_string().as_bytes())
        .and_then(|()| stream.write_all(b"\n"))
        .map_err(|e| format!("write {}: {e}", socket.display()))?;

    let mut reader = BufReader::new(stream);
    let mut line = String::new();
    let n = reader
        .read_line(&mut line)
        .map_err(|e| format!("read {}: {e}", socket.display()))?;
    if n == 0 {
        return Err("daemon closed the connection without responding".into());
    }
    serde_json::from_str(&line).map_err(|e| format!("invalid daemon response: {e}: {line}"))
}

/// Liveness probe: connect and run a real `bootstrap`. A leftover socket file
/// from a crash answers `connect` but never reads, which the read timeout
/// turns into `false`.
fn daemon_alive(project_root: &str) -> bool {
    let socket = socket_path(project_root);
    let mut stream = match UnixStream::connect(&socket) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let _ = stream.set_read_timeout(Some(Duration::from_secs(5)));
    let ping = json!({"cmd": "bootstrap", "project_root": project_root}).to_string();
    if stream.write_all(ping.as_bytes()).and_then(|()| stream.write_all(b"\n")).is_err() {
        return false;
    }
    let mut line = String::new();
    match BufReader::new(stream).read_line(&mut line) {
        Ok(n) if n > 0 => serde_json::from_str::<Value>(&line)
            .map(|v| v.get("ok").and_then(Value::as_bool) == Some(true))
            .unwrap_or(false),
        _ => false,
    }
}

/// Locate a `go` toolchain for the missing-binary convenience rebuild. A
/// GUI-launched app on macOS inherits a minimal PATH, so common install
/// locations are tried after the shell's.
fn go_tool() -> Option<PathBuf> {
    for cmd in ["go", "/usr/local/go/bin/go", "/opt/homebrew/bin/go", "/usr/local/bin/go"] {
        let ok = Command::new(cmd)
            .arg("version")
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        if ok {
            return Some(PathBuf::from(cmd));
        }
    }
    if let Some(home) = std::env::var_os("HOME") {
        let candidate = Path::new(&home).join("go").join("bin").join("go");
        if candidate.exists() {
            return Some(candidate);
        }
    }
    None
}

/// Start the daemon if the socket is not already answering. Idempotent and
/// safe to call on every command failure.
fn ensure_daemon_running(project_root: &str) -> Result<(), String> {
    if daemon_alive(project_root) {
        return Ok(());
    }

    let binary = daemon_binary(project_root);
    if !binary.exists() {
        // Convenience rebuild so a fresh clone works out of the box.
        if let Some(go) = go_tool() {
            let _ = Command::new(go)
                .args(["build", "-o", "odo", "."])
                .current_dir(project_root)
                .stdin(Stdio::null())
                .status();
        }
        if !binary.exists() {
            return Err(format!(
                "daemon binary missing at {}; build it with `go build -o odo .` in {project_root}",
                binary.display()
            ));
        }
    }

    // Daemon logs land in <project>/.odo/daemon.log so startup failures are
    // diagnosable without a console attached to the app.
    let state_dir = Path::new(project_root).join(".odo");
    std::fs::create_dir_all(&state_dir).map_err(|e| format!("create {}: {e}", state_dir.display()))?;
    let log_path = state_dir.join("daemon.log");
    let log = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)
        .map_err(|e| format!("open {}: {e}", log_path.display()))?;
    let log_err = log
        .try_clone()
        .map_err(|e| format!("clone {}: {e}", log_path.display()))?;

    Command::new(&binary)
        .arg("-project")
        .arg(project_root)
        .current_dir(project_root)
        .stdin(Stdio::null())
        .stdout(Stdio::from(log))
        .stderr(Stdio::from(log_err))
        .spawn()
        .map_err(|e| format!("spawn daemon {}: {e}", binary.display()))?;

    let deadline = std::time::Instant::now() + STARTUP_TIMEOUT;
    while std::time::Instant::now() < deadline {
        if daemon_alive(project_root) {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    Err(format!(
        "daemon did not answer on {} within {STARTUP_TIMEOUT:?} (see {})",
        socket_path(project_root).display(),
        log_path.display()
    ))
}

/// Round trip with one recovery retry: the first failure usually means the
/// daemon died (or was never started), so ensure it's up and try once more.
fn send_to_daemon(project_root: &str, req: &Value) -> Result<Value, String> {
    match round_trip(project_root, req) {
        Ok(resp) => Ok(resp),
        Err(first) => {
            if let Err(e) = ensure_daemon_running(project_root) {
                return Err(format!("{first} (daemon restart failed: {e})"));
            }
            round_trip(project_root, req)
        }
    }
}

/// Execute a command off the async runtime's workers: socket IO is blocking.
async fn run_command(project_root: String, req: Value) -> Result<Value, String> {
    tauri::async_runtime::spawn_blocking(move || send_to_daemon(&project_root, &req))
        .await
        .map_err(|e| format!("command task failed: {e}"))?
}

#[tauri::command]
async fn bootstrap(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "bootstrap", "project_root": root});
    run_command(root, req).await
}

#[tauri::command]
async fn send_message(
    conversation_id: i64,
    text: String,
    attachments: Option<Vec<String>>,
) -> Result<Value, String> {
    let root = default_project_root()?;
    // The daemon ignores `attachments` today (its Request struct has no such
    // field, and Go JSON decoding drops unknown keys); the frontend already
    // prefixes the paths into `text`. Forwarded now so the daemon-side
    // change needs no further GUI work.
    let mut req = json!({"cmd": "send_message", "conversation_id": conversation_id, "text": text});
    if let Some(paths) = attachments {
        req["attachments"] = json!(paths);
    }
    run_command(root, req).await
}

#[tauri::command]
async fn poll_events(conversation_id: i64, after_seq: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "poll_events", "conversation_id": conversation_id, "after_seq": after_seq});
    run_command(root, req).await
}

#[tauri::command]
async fn accept_diff(diff_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "accept_diff", "diff_id": diff_id});
    run_command(root, req).await
}

#[tauri::command]
async fn reject_diff(diff_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "reject_diff", "diff_id": diff_id});
    run_command(root, req).await
}

#[cfg(test)]
mod tests {
    use super::*;

    /// End-to-end round trip against a real daemon. Requires a daemon bound
    /// to a throwaway git repo with a stub OMP wrapper, e.g.:
    ///   ODO_OMP_WRAPPER=/path/to/stub.sh ./odo -project /tmp/odo-smoke
    /// then `ODO_SMOKE_ROOT=/tmp/odo-smoke cargo test`. Skipped otherwise.
    fn smoke_root() -> Option<String> {
        std::env::var("ODO_SMOKE_ROOT").ok().filter(|r| daemon_alive(r))
    }

    #[test]
    fn full_visible_loop() {
        let Some(root) = smoke_root() else {
            eprintln!("skipping: ODO_SMOKE_ROOT daemon not available");
            return;
        };

        let boot = send_to_daemon(&root, &json!({"cmd": "bootstrap", "project_root": root})).unwrap();
        assert_eq!(boot["ok"], true);
        let cid = boot["conversation"]["id"].as_i64().unwrap();

        let sent = send_to_daemon(
            &root,
            &json!({"cmd": "send_message", "conversation_id": cid, "text": "smoke: create a file"}),
        )
        .unwrap();
        assert_eq!(sent["ok"], true);
        assert_eq!(sent["event"]["type"], "user_message");

        // Poll until the run finishes and a pending diff appears.
        let mut seq = sent["event"]["seq"].as_i64().unwrap();
        let mut diff_id = 0;
        for _ in 0..100 {
            let poll = send_to_daemon(
                &root,
                &json!({"cmd": "poll_events", "conversation_id": cid, "after_seq": seq}),
            )
            .unwrap();
            assert_eq!(poll["ok"], true);
            for ev in poll["events"].as_array().map(Vec::as_slice).unwrap_or(&[]) {
                seq = seq.max(ev["seq"].as_i64().unwrap());
            }
            match poll["agent_running"].as_bool() {
                Some(false) => {
                    let done = poll["events"]
                        .as_array()
                        .map(|es| es.iter().any(|e| e["type"] == "agent_done" || e["type"] == "review_action"))
                        .unwrap_or(false);
                    if done || poll["diff"]["status"] == "pending" {
                        if poll["diff"]["status"] == "pending" {
                            diff_id = poll["diff"]["id"].as_i64().unwrap();
                        }
                        break;
                    }
                }
                _ => {}
            }
            std::thread::sleep(std::time::Duration::from_millis(200));
        }
        assert!(diff_id > 0, "stub run should produce a pending diff");

        let accepted = send_to_daemon(&root, &json!({"cmd": "accept_diff", "diff_id": diff_id})).unwrap();
        assert_eq!(accepted, json!({"ok": true, "diff_id": diff_id, "applied": true}));

        // Review is single-shot: a second accept must fail.
        let again = send_to_daemon(&root, &json!({"cmd": "accept_diff", "diff_id": diff_id})).unwrap();
        assert_eq!(again["ok"], false);

        // Session restore: bootstrap replays the journal including the review.
        let reboot = send_to_daemon(&root, &json!({"cmd": "bootstrap", "project_root": root})).unwrap();
        let reviews = reboot["events"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|e| e["type"] == "review_action")
            .count();
        assert!(reviews >= 1);
        assert_eq!(reboot["diff"]["status"], "accepted");
    }

    #[test]
    fn unknown_diff_errors_cleanly() {
        let Some(root) = smoke_root() else {
            eprintln!("skipping: ODO_SMOKE_ROOT daemon not available");
            return;
        };
        let resp = send_to_daemon(&root, &json!({"cmd": "reject_diff", "diff_id": 999999})).unwrap();
        assert_eq!(resp["ok"], false);
        assert!(resp["error"].as_str().unwrap().contains("999999"));
    }
}

pub fn run() {
    // Best-effort pre-start so the daemon warms up before the first UI call;
    // failures surface as command errors in the UI rather than aborting boot.
    if let Err(e) = default_project_root().and_then(|root| {
        eprintln!("odo: project root {root}");
        ensure_daemon_running(&root)
    }) {
        eprintln!("odo: daemon pre-start failed: {e}");
    }

    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            bootstrap,
            send_message,
            poll_events,
            accept_diff,
            reject_diff
        ])
        .run(tauri::generate_context!())
        .expect("error while running odo");
}
