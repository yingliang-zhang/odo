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

/// `distill` runs a full summary-agent turn (bounded by the daemon's
/// 10-minute `distillTimeout`) followed by the M4 learner one-shot (bounded
/// by the 5-minute `learnerTimeout`) — both synchronously before the daemon
/// answers, and the daemon serves one connection at a time, so the read
/// timeout covers both plus margin (10m + 5m + margin).
const DISTILL_READ_TIMEOUT: Duration = Duration::from_secs(960);

/// `curate` runs the curator one-shot (bounded by the daemon's 10-minute
/// `curatorTimeout`): it reads up to 50 epoch notes and rewrites every topic
/// page plus wiki/index.md synchronously before answering — 10m + margin.
const CURATE_READ_TIMEOUT: Duration = Duration::from_secs(660);

/// M2: `review_diff` waits on every configured review model daemon-side
/// (sequentially in the worst case). Five minutes plus margin covers the
/// expected multi-model latency without hanging the UI forever.
const REVIEW_READ_TIMEOUT: Duration = Duration::from_secs(330);

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
fn round_trip(project_root: &str, req: &Value, read_timeout: Duration) -> Result<Value, String> {
    let socket = socket_path(project_root);
    let mut stream = UnixStream::connect(&socket).map_err(|e| format!("connect {}: {e}", socket.display()))?;
    let _ = stream.set_read_timeout(Some(read_timeout));

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
fn send_to_daemon(project_root: &str, req: &Value, read_timeout: Duration) -> Result<Value, String> {
    match round_trip(project_root, req, read_timeout) {
        Ok(resp) => Ok(resp),
        Err(first) => {
            if let Err(e) = ensure_daemon_running(project_root) {
                return Err(format!("{first} (daemon restart failed: {e})"));
            }
            round_trip(project_root, req, read_timeout)
        }
    }
}

/// Execute a command off the async runtime's workers: socket IO is blocking.
async fn run_command(project_root: String, req: Value, read_timeout: Duration) -> Result<Value, String> {
    tauri::async_runtime::spawn_blocking(move || send_to_daemon(&project_root, &req, read_timeout))
        .await
        .map_err(|e| format!("command task failed: {e}"))?
}

#[tauri::command]
async fn bootstrap(project_root: Option<String>, workstream_id: Option<i64>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let mut req = json!({"cmd": "bootstrap", "project_root": root});
    // M1: when set, bootstrap targets that workstream's latest conversation
    // (workstream switch in the sidebar).
    if let Some(id) = workstream_id {
        req["workstream_id"] = json!(id);
    }
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn send_message(
    conversation_id: i64,
    text: String,
    attachments: Option<Vec<String>>,
    steer: Option<bool>,
    adapter: Option<String>,
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
    // M1: steer journals the message for the running agent without starting
    // a new run; adapter selects the backend ("omp" | "pi").
    if let Some(steer) = steer {
        req["steer"] = json!(steer);
    }
    if let Some(adapter) = adapter {
        if !adapter.is_empty() {
            req["adapter"] = json!(adapter);
        }
    }
    run_command(root, req, READ_TIMEOUT).await
}

// Belt A: abort the conversation's active run (adapter SIGKILLs the
// process group); the daemon journals agent_error{cancelled by user} and
// the drain path settles the run on the next poll.
#[tauri::command]
async fn cancel(conversation_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "cancel", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn create_workstream(project_root: Option<String>, name: String) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "create_workstream", "project_root": root, "name": name});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn list_workstreams(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "list_workstreams", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn distill(conversation_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    // The daemon's distillTimeout is 10 minutes and it serves one connection
    // at a time: this command holds the daemon until the note is written.
    // The frontend pauses its poll loop while a distill is in flight.
    let req = json!({"cmd": "distill", "conversation_id": conversation_id});
    run_command(root, req, DISTILL_READ_TIMEOUT).await
}

#[tauri::command]
async fn poll_events(conversation_id: i64, after_seq: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "poll_events", "conversation_id": conversation_id, "after_seq": after_seq});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn accept_diff(diff_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "accept_diff", "diff_id": diff_id});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn reject_diff(diff_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "reject_diff", "diff_id": diff_id});
    run_command(root, req, READ_TIMEOUT).await
}

// M2: ask the configured review models to grade the pending diff. Blocks
// daemon-side until every reviewer answers, hence the long read timeout.
#[tauri::command]
async fn review_diff(diff_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "review_diff", "diff_id": diff_id});
    run_command(root, req, REVIEW_READ_TIMEOUT).await
}

// M2 settings: read the project-scoped settings file through the daemon.
#[tauri::command]
async fn get_settings(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "get_settings", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M2 settings: `settings` is forwarded verbatim; the daemon validates and
// merges it (unknown keys are its problem, not this shell's).
#[tauri::command]
async fn update_settings(project_root: Option<String>, settings: Value) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "update_settings", "project_root": root, "settings": settings});
    run_command(root, req, READ_TIMEOUT).await
}

// M2 fan-out: start N parallel agent runs on one prompt. The daemon returns
// immediately with the run list; progress arrives through the poll loop.
#[tauri::command]
async fn fanout_send(conversation_id: i64, text: String, n: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "fanout_send", "conversation_id": conversation_id, "text": text, "n": n});
    run_command(root, req, READ_TIMEOUT).await
}

// M3 wiki browser: list the workstream's distilled notes (read-only).
#[tauri::command]
async fn list_wiki(conversation_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "list_wiki", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
}

// M3 wiki browser: read one note (or ~/.odo/user.md) through the daemon,
// which enforces the wiki/-only path guard.
#[tauri::command]
async fn read_wiki(path: String) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "read_wiki", "path": path});
    run_command(root, req, READ_TIMEOUT).await
}

// M3 visibility (spec §3c): project-wide pending-diff counts and running
// workstreams. poll_events is per-conversation, so this read-only fallback
// is the sidebar's only view into other workstreams.
#[tauri::command]
async fn pending_counts(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "pending_counts", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M4 learning: read the three canonical memory files (project memory.md,
// memory-archive.md, global user.md) through the daemon, which constructs
// the paths itself and equality-checks the root against its bound root.
#[tauri::command]
async fn read_memory(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "read_memory", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M4 learning: the conversation's pending learner-proposal batch
// (journal-only storage; no batch fields in the response = nothing pending).
#[tauri::command]
async fn memory_proposals(conversation_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "memory_proposals", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
}

// M4 learning: apply the accepted subset of the pending batch. Blocks on
// daemon-side atomic file writes (memory.md/user.md rewrites + archive
// append) plus journal appends, so it gets a review-length timeout rather
// than the generic 120 s. `accepted` is forwarded verbatim, like settings.
#[tauri::command]
async fn apply_memory(conversation_id: i64, epoch: i64, accepted: Value) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "apply_memory", "conversation_id": conversation_id, "epoch": epoch, "accepted": accepted});
    run_command(root, req, REVIEW_READ_TIMEOUT).await
}

// M5 curation: the curator one-shot rewrites wiki/topics/*.md + wiki/index.md
// from the full epoch-note set (generation-2 rule). Blocks daemon-side like
// distill, hence the curator-length read timeout; the frontend pauses its
// poll loop while a curate is in flight.
#[tauri::command]
async fn curate(conversation_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "curate", "conversation_id": conversation_id});
    run_command(root, req, CURATE_READ_TIMEOUT).await
}

// M5 curation: store one verbatim pin line in .odo/pins.md (no LLM
// processing; overflow refuses with an error naming the pin text).
#[tauri::command]
async fn pin(conversation_id: i64, text: String) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "pin", "conversation_id": conversation_id, "text": text});
    run_command(root, req, READ_TIMEOUT).await
}

// M5 curation: read .odo/pins.md through the daemon (same resolve-root guard
// and memory_content field as read_memory; "" when the file is absent).
#[tauri::command]
async fn read_pins(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "read_pins", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M5 curation: list wiki/topics/*.md pages (title parsed from the first `# `
// line) through the daemon's list_topics command.
#[tauri::command]
async fn list_topics(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "list_topics", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M6 precision+ledger: read .odo/ledger.md through the daemon (same shape
// and memory_content field as read_pins; "" when the file is absent).
// Read-only, generic READ_TIMEOUT.
#[tauri::command]
async fn ledger(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "ledger", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M6 precision+ledger: the conversation's note-retraction events
// (memory_update{layer:"note", cause:"retract"}) for the wiki browser's
// retracted badges. Read-only, generic READ_TIMEOUT.
#[tauri::command]
async fn contradictions(conversation_id: i64) -> Result<Value, String> {
    let root = default_project_root()?;
    let req = json!({"cmd": "contradictions", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
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

        let boot = send_to_daemon(&root, &json!({"cmd": "bootstrap", "project_root": root}), READ_TIMEOUT).unwrap();
        assert_eq!(boot["ok"], true);
        let cid = boot["conversation"]["id"].as_i64().unwrap();

        let sent = send_to_daemon(
            &root,
            &json!({"cmd": "send_message", "conversation_id": cid, "text": "smoke: create a file"}),
            READ_TIMEOUT,
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
                READ_TIMEOUT,
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

        let accepted = send_to_daemon(&root, &json!({"cmd": "accept_diff", "diff_id": diff_id}), READ_TIMEOUT).unwrap();
        assert_eq!(accepted, json!({"ok": true, "diff_id": diff_id, "applied": true}));

        // Review is single-shot: a second accept must fail.
        let again = send_to_daemon(&root, &json!({"cmd": "accept_diff", "diff_id": diff_id}), READ_TIMEOUT).unwrap();
        assert_eq!(again["ok"], false);

        // Session restore: bootstrap replays the journal including the review.
        let reboot = send_to_daemon(&root, &json!({"cmd": "bootstrap", "project_root": root}), READ_TIMEOUT).unwrap();
        let reviews = reboot["events"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|e| e["type"] == "review_action")
            .count();
        assert!(reviews >= 1);
        assert_eq!(reboot["diff"]["status"], "accepted");
    }

    /// M1 flow end to end over the socket layer: workstream create/list,
    /// bootstrap switch, adapter selection, steering, distill. The request
    /// shapes mirror the JSON the Tauri commands above assemble.
    #[test]
    fn m1_workstream_steer_distill() {
        let Some(root) = smoke_root() else {
            eprintln!("skipping: ODO_SMOKE_ROOT daemon not available");
            return;
        };

        let boot = send_to_daemon(&root, &json!({"cmd": "bootstrap", "project_root": root}), READ_TIMEOUT).unwrap();
        assert_eq!(boot["ok"], true);
        let main_ws = boot["workstream"]["id"].as_i64().unwrap();

        let listed = send_to_daemon(&root, &json!({"cmd": "list_workstreams", "project_root": root}), READ_TIMEOUT).unwrap();
        assert_eq!(listed["ok"], true);
        assert!(listed["workstreams"].as_array().unwrap().iter().any(|w| w["id"].as_i64() == Some(main_ws)));

        let created = send_to_daemon(
            &root,
            &json!({"cmd": "create_workstream", "project_root": root, "name": "smoke refactor"}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(created["ok"], true);
        let ws2 = created["workstream"]["id"].as_i64().unwrap();
        // The daemon sanitizes names into git-safe branch names.
        assert_eq!(created["workstream"]["name"], "smoke-refactor");

        // Workstream switch: bootstrap targeted at the new workstream returns
        // a fresh conversation with an empty journal.
        let switched = send_to_daemon(
            &root,
            &json!({"cmd": "bootstrap", "project_root": root, "workstream_id": ws2}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(switched["ok"], true);
        assert_eq!(switched["workstream"]["id"].as_i64(), Some(ws2));
        let cid = switched["conversation"]["id"].as_i64().unwrap();
        assert_eq!(switched["conversation"]["epoch"], 1);

        // Start a run on the Pi adapter.
        let sent = send_to_daemon(
            &root,
            &json!({"cmd": "send_message", "conversation_id": cid, "text": "smoke m1", "adapter": "pi"}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(sent["ok"], true);

        // Steer while the stub run is in flight: the message is journaled as
        // a user_message without starting a second run.
        let steered = send_to_daemon(
            &root,
            &json!({"cmd": "send_message", "conversation_id": cid, "text": "and also this", "steer": true}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(steered["ok"], true);
        assert_eq!(steered["event"]["type"], "user_message");

        // Wait out the stub agent (same poll loop as full_visible_loop).
        // Adapters without mid-run steering support journal an agent_error;
        // either way the steering user_message stays put.
        std::thread::sleep(Duration::from_secs(1));
        let mut seq = steered["event"]["seq"].as_i64().unwrap_or(1);
        for _ in 0..50 {
            let poll = send_to_daemon(
                &root,
                &json!({"cmd": "poll_events", "conversation_id": cid, "after_seq": seq}),
                READ_TIMEOUT,
            )
            .unwrap();
            for ev in poll["events"].as_array().map(Vec::as_slice).unwrap_or(&[]) {
                seq = seq.max(ev["seq"].as_i64().unwrap());
            }
            if poll["agent_running"].as_bool() == Some(false) {
                break;
            }
            std::thread::sleep(Duration::from_millis(200));
        }

        // A second (non-steer) send while the agent ran must have been
        // impossible; only the steering path got queued. The stub always
        // finishes, so distill is allowed now.
        let distilled = send_to_daemon(&root, &json!({"cmd": "distill", "conversation_id": cid}), DISTILL_READ_TIMEOUT).unwrap();
        assert_eq!(distilled["ok"], true);
        assert_eq!(distilled["epoch"], 2);
        let wiki = distilled["wiki_path"].as_str().unwrap();
        assert!(wiki.contains("/wiki/") && wiki.ends_with("-epoch-1.md"));
        assert!(std::path::Path::new(wiki).exists());

        // The epoch bump is visible on the next bootstrap targeted here.
        let reboot = send_to_daemon(
            &root,
            &json!({"cmd": "bootstrap", "workstream_id": ws2, "project_root": root}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(reboot["conversation"]["epoch"], 2);
    }

    #[test]
    fn unknown_diff_errors_cleanly() {
        let Some(root) = smoke_root() else {
            eprintln!("skipping: ODO_SMOKE_ROOT daemon not available");
            return;
        };
        let resp = send_to_daemon(&root, &json!({"cmd": "reject_diff", "diff_id": 999999}), READ_TIMEOUT).unwrap();
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
        .plugin(tauri_plugin_notification::init())
        .invoke_handler(tauri::generate_handler![
            bootstrap,
            send_message,
            cancel,
            poll_events,
            accept_diff,
            reject_diff,
            create_workstream,
            list_workstreams,
            distill,
            review_diff,
            get_settings,
            update_settings,
            fanout_send,
            list_wiki,
            read_wiki,
            pending_counts,
            read_memory,
            memory_proposals,
            apply_memory,
            curate,
            pin,
            read_pins,
            list_topics,
            ledger,
            contradictions
        ])
        .run(tauri::generate_context!())
        .expect("error while running odo");
}
