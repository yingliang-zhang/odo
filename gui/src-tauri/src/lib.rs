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
/// by the 5-minute `learnerTimeout`) and the M9 tri-model skill gate
/// (bounded by the 5-minute skill-gate HTTP client timeout) — all synchronously before
/// the daemon answers. The older rationale cited the daemon serving "one
/// connection at a time"; since M11 each call is a fresh connection served
/// by its own goroutine, but the chain still runs synchronously per call,
/// so the read timeout covers all three plus margin (10m + 5m + 5m + margin).
const DISTILL_READ_TIMEOUT: Duration = Duration::from_secs(1900);

/// `curate` runs the curator one-shot (bounded by the daemon's 10-minute
/// `curatorTimeout`): it reads up to 50 epoch notes and rewrites every topic
/// page plus wiki/index.md synchronously before answering — 10m + margin.
const CURATE_READ_TIMEOUT: Duration = Duration::from_secs(660);

/// M2: `review_diff` waits on every configured review model daemon-side
/// (sequentially in the worst case). Five minutes plus margin covers the
/// expected multi-model latency without hanging the UI forever.
const REVIEW_READ_TIMEOUT: Duration = Duration::from_secs(330);

/// /panel and /vision fan out to N models with up to 16 tool rounds each —
/// the slowest observed real /panel took 393s (3 models + FS tools), past
/// REVIEW_READ_TIMEOUT. The duplicate-dispatch vector is gone (read-stage
/// failures no longer retry), so the timeout only bounds UI patience: a
/// slower panel surfaces an invoke error while the daemon still journals
/// the answer for the next poll. 700s ≈ worst observed × 1.8.
const SLASH_READ_TIMEOUT: Duration = Duration::from_secs(700);

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
    // 1. <project>/odo — the primary location (convenience rebuild target)
    let local = Path::new(project_root).join("odo");
    if local.exists() {
        return local;
    }
    // 2. Walk up from the GUI binary's directory looking for a sibling
    //    `odo` binary. The Tauri app and daemon are built from the same
    //    repo, so the GUI's ancestor directories likely contain it.
    if let Ok(exe) = std::env::current_exe() {
        let mut dir = exe.parent();
        while let Some(d) = dir {
            let candidate = d.join("odo");
            if candidate.exists() {
                return candidate;
            }
            dir = d.parent();
        }
    }
    // 3. ~/.odo/bin/odo — global install path
    if let Some(home) = std::env::var_os("HOME") {
        let global = Path::new(&home).join(".odo").join("bin").join("odo");
        if global.exists() {
            return global;
        }
    }
    // Fallback: the original path (will trigger the rebuild attempt)
    local
}

/// A failed round trip, tagged by side-effect safety for the recovery
/// retry in send_to_daemon. Only failures BEFORE the daemon could hold a
/// complete request — connect refused (daemon down / stale socket) or a
/// broken write (partial line + EOF on its read side — never parsed) — may
/// be retried. Once the daemon accepted the request, a read timeout means
/// "still working", and retrying re-executes a non-idempotent command:
/// send_message journals a second user_message and (for /panel) doubles
/// the API spend — observed 2026-08-11 as a duplicated /panel answer when
/// a 393s run exceeded the 330s bridge timeout and the retry fired at
/// exactly timeout time.
struct RoundTripError {
    retryable: bool,
    message: String,
}

impl RoundTripError {
    fn retryable(message: String) -> Self {
        Self {
            retryable: true,
            message,
        }
    }
    fn terminal(message: String) -> Self {
        Self {
            retryable: false,
            message,
        }
    }
}

/// Single request → single response on a fresh connection. The daemon serves
/// each connection on its own goroutine (M11 P0), so concurrent calls no
/// longer queue behind one another; every call connects, exchanges, and drops.
fn round_trip(
    project_root: &str,
    req: &Value,
    read_timeout: Duration,
) -> Result<Value, RoundTripError> {
    let socket = socket_path(project_root);
    let mut stream = UnixStream::connect(&socket)
        .map_err(|e| RoundTripError::retryable(format!("connect {}: {e}", socket.display())))?;
    let _ = stream.set_read_timeout(Some(read_timeout));

    stream
        .write_all(req.to_string().as_bytes())
        .and_then(|()| stream.write_all(b"\n"))
        .map_err(|e| RoundTripError::retryable(format!("write {}: {e}", socket.display())))?;

    let mut reader = BufReader::new(stream);
    let mut line = String::new();
    let n = reader
        .read_line(&mut line)
        .map_err(|e| RoundTripError::terminal(format!("read {}: {e}", socket.display())))?;
    if n == 0 {
        return Err(RoundTripError::terminal(
            "daemon closed the connection without responding".into(),
        ));
    }
    serde_json::from_str(&line)
        .map_err(|e| RoundTripError::terminal(format!("invalid daemon response: {e}: {line}")))
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
    if stream
        .write_all(ping.as_bytes())
        .and_then(|()| stream.write_all(b"\n"))
        .is_err()
    {
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
    for cmd in [
        "go",
        "/usr/local/go/bin/go",
        "/opt/homebrew/bin/go",
        "/usr/local/bin/go",
    ] {
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
    std::fs::create_dir_all(&state_dir)
        .map_err(|e| format!("create {}: {e}", state_dir.display()))?;
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

/// Round trip with one recovery retry — only when the request never
/// reached the daemon (connect/write failure: it died or was never
/// started, so ensure it's up and try once). Read-stage failures are
/// returned as-is: the daemon may still be executing, and re-dispatching
/// non-idempotent commands duplicates their side effects.
fn send_to_daemon(
    project_root: &str,
    req: &Value,
    read_timeout: Duration,
) -> Result<Value, String> {
    match round_trip(project_root, req, read_timeout) {
        Ok(resp) => Ok(resp),
        Err(first) => {
            if !first.retryable {
                return Err(first.message);
            }
            if let Err(e) = ensure_daemon_running(project_root) {
                return Err(format!("{} (daemon restart failed: {e})", first.message));
            }
            round_trip(project_root, req, read_timeout).map_err(|e| e.message)
        }
    }
}

/// Execute a command off the async runtime's workers: socket IO is blocking.
async fn run_command(
    project_root: String,
    req: Value,
    read_timeout: Duration,
) -> Result<Value, String> {
    tauri::async_runtime::spawn_blocking(move || send_to_daemon(&project_root, &req, read_timeout))
        .await
        .map_err(|e| format!("command task failed: {e}"))?
}

#[tauri::command]
async fn bootstrap(
    project_root: Option<String>,
    workstream_id: Option<i64>,
) -> Result<Value, String> {
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
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    // The daemon honors `attachments` for /vision (image content blocks).
    let mut req = json!({"cmd": "send_message", "conversation_id": conversation_id, "text": text});
    if let Some(paths) = attachments {
        req["attachments"] = json!(paths);
    }
    // M1: steer journals the message for the running agent without starting
    // a new run; adapter selects the backend ("omp").
    if let Some(steer) = steer {
        req["steer"] = json!(steer);
    }
    if let Some(adapter) = adapter {
        if !adapter.is_empty() {
            req["adapter"] = json!(adapter);
        }
    }
    // /panel and /vision fan out to N models with FS-tool rounds; the
    // generic 120s timeout provably cut real panels mid-run.
    let timeout = if text.starts_with("/panel") || text.starts_with("/vision") {
        SLASH_READ_TIMEOUT
    } else {
        READ_TIMEOUT
    };
    run_command(root, req, timeout).await
}

// Belt A: abort the conversation's active run (adapter SIGKILLs the
// process group); the daemon journals agent_error{cancelled by user} and
// the drain path settles the run on the next poll.
#[tauri::command]
async fn cancel(conversation_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
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
async fn rename_workstream(
    project_root: Option<String>,
    workstream_id: i64,
    name: String,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "rename_workstream", "project_root": root, "workstream_id": workstream_id, "name": name});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn delete_workstream(
    project_root: Option<String>,
    workstream_id: i64,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req =
        json!({"cmd": "delete_workstream", "project_root": root, "workstream_id": workstream_id});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn distill(conversation_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    // The daemon's distillTimeout is 10 minutes and the chain runs
    // synchronously before the daemon answers this call. ("One connection
    // at a time" was the pre-M11 rationale; today each call is a fresh
    // connection served by its own goroutine, but this call's response
    // still waits for the whole chain.) The frontend pauses its own poll
    // loop while a distill is in flight instead of queueing up certain
    // timeout failures.
    let req = json!({"cmd": "distill", "conversation_id": conversation_id});
    run_command(root, req, DISTILL_READ_TIMEOUT).await
}

#[tauri::command]
async fn poll_events(
    conversation_id: i64,
    after_seq: i64,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req =
        json!({"cmd": "poll_events", "conversation_id": conversation_id, "after_seq": after_seq});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn accept_diff(diff_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "accept_diff", "diff_id": diff_id});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn reject_diff(diff_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "reject_diff", "diff_id": diff_id});
    run_command(root, req, READ_TIMEOUT).await
}

// M2: ask the configured review models to grade the pending diff. Blocks
// daemon-side until every reviewer answers, hence the long read timeout.
#[tauri::command]
async fn review_diff(diff_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
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

// M3 wiki browser: list the workstream's distilled notes (read-only).
#[tauri::command]
async fn list_wiki(conversation_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "list_wiki", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
}

// M3 wiki browser: read one note (or ~/.odo/user.md) through the daemon,
// which enforces the wiki/-only path guard.
#[tauri::command]
async fn read_wiki(path: String, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
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

// M15 (O-1 rung-0): the autonomy streak snapshot the DiffViewer header
// shows on open; a read-only journal computation daemon-side.
#[tauri::command]
async fn autonomy_status(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "autonomy_status", "project_root": root});
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
async fn memory_proposals(
    conversation_id: i64,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "memory_proposals", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
}

// M4 learning: apply the accepted subset of the pending batch. Blocks on
// daemon-side atomic file writes (memory.md/user.md rewrites + archive
// append) plus journal appends, so it gets a review-length timeout rather
// than the generic 120 s. `accepted` is forwarded verbatim, like settings.
#[tauri::command]
async fn apply_memory(
    conversation_id: i64,
    epoch: i64,
    accepted: Value,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "apply_memory", "conversation_id": conversation_id, "epoch": epoch, "accepted": accepted});
    run_command(root, req, REVIEW_READ_TIMEOUT).await
}

// M5 curation: the curator one-shot rewrites wiki/topics/*.md + wiki/index.md
// from the full epoch-note set (generation-2 rule). Blocks daemon-side like
// distill, hence the curator-length read timeout; the frontend pauses its
// poll loop while a curate is in flight.
#[tauri::command]
async fn curate(conversation_id: i64, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "curate", "conversation_id": conversation_id});
    run_command(root, req, CURATE_READ_TIMEOUT).await
}

// M12 (D-auto): the composer countdown chip's Cancel — disarm a scheduled
// (not yet fired) auto-distill for one conversation. The daemon journals
// the disarm; in-flight auto distills are cancelled by sends instead.
#[tauri::command]
async fn auto_distill_ctl(
    conversation_id: i64,
    action: String,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req =
        json!({"cmd": "auto_distill_ctl", "conversation_id": conversation_id, "action": action});
    run_command(root, req, READ_TIMEOUT).await
}

// M5 curation: store one verbatim pin line in .odo/pins.md (no LLM
// processing; overflow refuses with an error naming the pin text).
#[tauri::command]
async fn pin(
    conversation_id: i64,
    text: String,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "pin", "conversation_id": conversation_id, "text": text});
    run_command(root, req, READ_TIMEOUT).await
}

// M12 (D-todo): one user op from the composer "Plan" popover. The daemon
// journals the merge with origin:"user"; semantic rejects land inside the
// journaled event, not as an IPC error.
#[tauri::command]
async fn todo_update(
    conversation_id: i64,
    action: String,
    todo_id: Option<String>,
    text: Option<String>,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let mut req =
        json!({"cmd": "todo_update", "conversation_id": conversation_id, "action": action});
    if let Some(id) = todo_id {
        req["todo_id"] = json!(id);
    }
    if let Some(text) = text {
        req["text"] = json!(text);
    }
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

// M8 (Skills): list all discovered skill metadata (global ~/.odo/skills/
// + project .odo/skills/). Read-only, generic READ_TIMEOUT.
#[tauri::command]
async fn list_skills(project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "list_skills", "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M8 (Skills): read the full markdown body of one skill file. The path is
// the SkillInfo.path from list_skills (filename only after sanitization).
#[tauri::command]
async fn read_skill(path: String, project_root: Option<String>) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "read_skill", "path": path, "project_root": root});
    run_command(root, req, READ_TIMEOUT).await
}

// M8 (Skills): create or overwrite a skill file (human-in-the-loop write
// path). The daemon routes to project (.odo/skills/) or global
// (~/.odo/skills/) based on the scope field.
#[tauri::command]
async fn update_skill(
    name: String,
    text: String,
    path: Option<String>,
    scope: Option<String>,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({
        "cmd": "update_skill",
        "name": name,
        "text": text,
        "path": path.unwrap_or_default(),
        "scope": scope.unwrap_or_default(),
        "project_root": root,
    });
    run_command(root, req, READ_TIMEOUT).await
}

// M8 (Skills): delete a skill file. Scope is explicit on the wire.
#[tauri::command]
async fn delete_skill(
    name: String,
    scope: Option<String>,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({
        "cmd": "delete_skill",
        "name": name,
        "scope": scope.unwrap_or_default(),
        "project_root": root,
    });
    run_command(root, req, READ_TIMEOUT).await
}

// A1: save_attachment — writes base64-encoded clipboard image to .odo/attachments/
#[tauri::command]
async fn save_attachment(
    name: String,
    data: String,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({
        "cmd": "save_attachment",
        "name": name,
        "data": data,
        "project_root": root,
    });
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
async fn contradictions(
    conversation_id: i64,
    project_root: Option<String>,
) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "contradictions", "conversation_id": conversation_id});
    run_command(root, req, READ_TIMEOUT).await
}

#[tauri::command]
async fn search_events(project_root: Option<String>, text: String) -> Result<Value, String> {
    let root = resolve_root(project_root)?;
    let req = json!({"cmd": "search_events", "project_root": root, "text": text});
    run_command(root, req, READ_TIMEOUT).await
}

// Registry file location — ODO_REGISTRY_PATH override, else
// <home>/.odo/projects.json (same resolution as the Go registry).
fn registry_file_path() -> Result<PathBuf, String> {
    match std::env::var_os("ODO_REGISTRY_PATH").filter(|p| !p.is_empty()) {
        Some(p) => Ok(PathBuf::from(p)),
        None => {
            let home = std::env::var_os("HOME").ok_or("cannot find home directory")?;
            Ok(Path::new(&home).join(".odo").join("projects.json"))
        }
    }
}

// Absent file → empty list; a parse failure surfaces as an error rather
// than degrading to empty (the daemon owns the format and would still
// boot on a corrupt file, so the UI should show the real problem).
fn read_registry(path: &Path) -> Result<Value, String> {
    if !path.exists() {
        return Ok(json!([]));
    }
    let content =
        std::fs::read_to_string(path).map_err(|e| format!("read {}: {e}", path.display()))?;
    serde_json::from_str(&content).map_err(|e| format!("parse {}: {e}", path.display()))
}

// Same discipline as Go's writeFileAtomic: temp file in the same dir,
// 0600, rename over the target. The tmp name carries nanos (not just the
// pid): Tauri runs commands on a threadpool, so two concurrent removals
// in one process must not share a tmp file. Permissions are set after
// open unconditionally — OpenOptions::mode only applies at creation, and
// a stale tmp from a crashed earlier run could otherwise smuggle 0644 in.
fn write_registry_atomic(path: &Path, contents: &str) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| format!("create {}: {e}", parent.display()))?;
    }
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let tmp = path.with_file_name(format!(
        ".{}.tmp-{}-{}",
        path.file_name()
            .unwrap_or_default()
            .to_string_lossy(),
        std::process::id(),
        nanos
    ));
    let write_result = (|| {
        let mut f = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&tmp)
            .map_err(|e| format!("create {}: {e}", tmp.display()))?;
        f.set_permissions(std::fs::Permissions::from_mode(0o600))
            .map_err(|e| format!("chmod {}: {e}", tmp.display()))?;
        f.write_all(contents.as_bytes())
            .map_err(|e| format!("write {}: {e}", tmp.display()))?;
        f.sync_all().ok();
        std::fs::rename(&tmp, path).map_err(|e| format!("rename onto {}: {e}", path.display()))
    })();
    if write_result.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    write_result
}

// Drop every row whose root matches (removal is idempotent: an absent
// root just returns the list unchanged). Returns the full updated list so
// the frontend can setState straight from the response.
fn remove_project_from(path: &Path, root: &str) -> Result<Value, String> {
    let rows = read_registry(path)?;
    let mut rows = rows
        .as_array()
        .cloned()
        .ok_or_else(|| format!("{}: registry is not a JSON array", path.display()))?;
    rows.retain(|r| r.get("root").and_then(|v| v.as_str()) != Some(root));
    let out = serde_json::to_string(&rows).map_err(|e| format!("marshal registry: {e}"))?;
    write_registry_atomic(path, &(out + "\n"))?;
    Ok(json!(rows))
}

// M11 P1: read-only view of the daemon-owned global registry
// (<home>/.odo/projects.json, ODO_REGISTRY_PATH override — same path
// resolution as the Go registry) for the sidebar project switcher.
#[tauri::command]
async fn list_projects() -> Result<Value, String> {
    read_registry(&registry_file_path()?)
}

// M11 F8 registry escape hatch: drop a project from the global registry.
// The 2026-08-11 phantom-project incident left a dead worktree row with
// no way out but hand-editing ~/.odo/projects.json (the GUI's 5s
// cross-project poll then respawned a stale daemon for it forever, and
// the daemon's NewServer re-registered the entry — the resurrection loop
// only ends once the row is gone AND the frontend stops polling it, see
// App.tsx handleRemoveProject). Removing a row does not touch the
// project's files or a running daemon; the Go-side worktree guard
// (ensureProjectRegistered) keeps phantom rows from coming back.
// The GUI refuses to offer this for the ACTIVE project (removal would be
// undone at that daemon's next boot); the command itself stays agnostic —
// switch away first, then remove.
#[tauri::command]
async fn remove_project(root: String) -> Result<Value, String> {
    remove_project_from(&registry_file_path()?, &root)
}

// M11 F1: open a native folder picker, ensure the daemon for that project is
// running (which auto-registers it in ~/.odo/projects.json via Go's
// ensureProjectRegistered), and return the new project entry so the frontend
// can refresh its list and switch to the new project.
#[tauri::command]
async fn add_project(app: tauri::AppHandle) -> Result<Option<Value>, String> {
    use tauri_plugin_dialog::DialogExt;
    let folder = app
        .dialog()
        .file()
        .set_title("Select a repository folder")
        .blocking_pick_folder();
    match folder {
        Some(path) => {
            let raw = path.to_string();
            // Go's ensureProjectRegistered stores EvalSymlinks-resolved
            // roots in the registry, so we must canonicalize the picked
            // path before looking it up — otherwise a symlinked folder
            // (e.g. /tmp → /private/tmp on macOS) silently fails to match.
            let root = std::fs::canonicalize(&raw)
                .map(|p| p.display().to_string())
                .unwrap_or(raw);
            ensure_daemon_running(&root)?;
            // Re-read the registry to get the full entry (with name + added).
            let projects = list_projects().await?;
            let entry = projects
                .as_array()
                .and_then(|rows| rows.iter().find(|r| r.get("root") == Some(&json!(root))));
            match entry {
                Some(e) => Ok(Some(e.clone())),
                None => Err(format!(
                    "daemon started for {} but project not found in registry (check ~/.odo/projects.json)",
                    root
                )),
            }
        }
        None => Ok(None),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // Registry-removal unit tests run without a daemon or a smoke root —
    // remove_project_from is pure file logic over an explicit path.
    fn temp_registry(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("odo-reg-test-{}-{}", std::process::id(), name));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir.join("projects.json")
    }

    fn seed_registry(path: &Path, roots: &[&str]) {
        let rows: Vec<Value> = roots
            .iter()
            .map(|r| json!({"root": r, "name": Path::new(r).file_name().unwrap(), "added": "2026-08-11T00:00:00Z"}))
            .collect();
        std::fs::write(path, serde_json::to_string(&rows).unwrap()).unwrap();
    }

    #[test]
    fn remove_project_drops_only_the_target_row() {
        let path = temp_registry("drop");
        seed_registry(&path, &["/a/main", "/a/main/.odo/worktrees/x", "/b/other"]);
        let out = remove_project_from(&path, "/a/main/.odo/worktrees/x").unwrap();
        let roots: Vec<&str> = out
            .as_array()
            .unwrap()
            .iter()
            .map(|r| r["root"].as_str().unwrap())
            .collect();
        assert_eq!(roots, ["/a/main", "/b/other"]);
        // The file on disk carries the same result (persistence, not just return value)…
        let on_disk = read_registry(&path).unwrap();
        assert_eq!(on_disk.as_array().unwrap().len(), 2);
        // …with owner-only permissions, matching Go's writeFileAtomic.
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
        }
        let _ = std::fs::remove_dir_all(path.parent().unwrap());
    }

    #[test]
    fn remove_project_absent_root_is_idempotent() {
        let path = temp_registry("absent");
        seed_registry(&path, &["/a/main"]);
        let out = remove_project_from(&path, "/never/registered").unwrap();
        assert_eq!(out.as_array().unwrap().len(), 1);
        let _ = std::fs::remove_dir_all(path.parent().unwrap());
    }

    #[test]
    fn remove_project_missing_file_writes_empty_list() {
        let path = temp_registry("missing");
        let out = remove_project_from(&path, "/anything").unwrap();
        assert_eq!(out.as_array().unwrap().len(), 0);
        assert!(path.exists());
        let _ = std::fs::remove_dir_all(path.parent().unwrap());
    }

    /// End-to-end round trip against a real daemon. Requires a daemon bound
    /// to a throwaway git repo with a stub OMP wrapper, e.g.:
    ///   ODO_OMP_WRAPPER=/path/to/stub.sh ./odo -project /tmp/odo-smoke
    /// then `ODO_SMOKE_ROOT=/tmp/odo-smoke cargo test`. Skipped otherwise.
    fn smoke_root() -> Option<String> {
        std::env::var("ODO_SMOKE_ROOT")
            .ok()
            .filter(|r| daemon_alive(r))
    }

    #[test]
    fn full_visible_loop() {
        let Some(root) = smoke_root() else {
            eprintln!("skipping: ODO_SMOKE_ROOT daemon not available");
            return;
        };

        let boot = send_to_daemon(
            &root,
            &json!({"cmd": "bootstrap", "project_root": root}),
            READ_TIMEOUT,
        )
        .unwrap();
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
                        .map(|es| {
                            es.iter()
                                .any(|e| e["type"] == "agent_done" || e["type"] == "review_action")
                        })
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

        let accepted = send_to_daemon(
            &root,
            &json!({"cmd": "accept_diff", "diff_id": diff_id}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(
            accepted,
            json!({"ok": true, "diff_id": diff_id, "applied": true})
        );

        // Review is single-shot: a second accept must fail.
        let again = send_to_daemon(
            &root,
            &json!({"cmd": "accept_diff", "diff_id": diff_id}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(again["ok"], false);

        // Session restore: bootstrap replays the journal including the review.
        let reboot = send_to_daemon(
            &root,
            &json!({"cmd": "bootstrap", "project_root": root}),
            READ_TIMEOUT,
        )
        .unwrap();
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
    /// bootstrap switch, steering, distill. The request
    /// shapes mirror the JSON the Tauri commands above assemble.
    #[test]
    fn m1_workstream_steer_distill() {
        let Some(root) = smoke_root() else {
            eprintln!("skipping: ODO_SMOKE_ROOT daemon not available");
            return;
        };

        let boot = send_to_daemon(
            &root,
            &json!({"cmd": "bootstrap", "project_root": root}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(boot["ok"], true);
        let main_ws = boot["workstream"]["id"].as_i64().unwrap();

        let listed = send_to_daemon(
            &root,
            &json!({"cmd": "list_workstreams", "project_root": root}),
            READ_TIMEOUT,
        )
        .unwrap();
        assert_eq!(listed["ok"], true);
        assert!(listed["workstreams"]
            .as_array()
            .unwrap()
            .iter()
            .any(|w| w["id"].as_i64() == Some(main_ws)));

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

        // Start a run on the OMP adapter.
        let sent = send_to_daemon(
            &root,
            &json!({"cmd": "send_message", "conversation_id": cid, "text": "smoke m1", "adapter": "omp"}),
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
        let distilled = send_to_daemon(
            &root,
            &json!({"cmd": "distill", "conversation_id": cid}),
            DISTILL_READ_TIMEOUT,
        )
        .unwrap();
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
        let resp = send_to_daemon(
            &root,
            &json!({"cmd": "reject_diff", "diff_id": 999999}),
            READ_TIMEOUT,
        )
        .unwrap();
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
        .plugin(tauri_plugin_dialog::init())
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
            rename_workstream,
            delete_workstream,
            distill,
            auto_distill_ctl,
            todo_update,
            review_diff,
            get_settings,
            update_settings,
            list_wiki,
            read_wiki,
            pending_counts,
            autonomy_status,
            read_memory,
            memory_proposals,
            apply_memory,
            curate,
            pin,
            read_pins,
            list_skills,
            read_skill,
            update_skill,
            delete_skill,
            save_attachment,
            list_topics,
            ledger,
            contradictions,
            search_events,
            list_projects,
            add_project,
            remove_project,
        ])
        .run(tauri::generate_context!())
        .expect("error while running odo");
}
