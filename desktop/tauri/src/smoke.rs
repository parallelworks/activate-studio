//! The smoke test's shell side (STUDIO_SMOKE=1 only). The page (smoke.js)
//! drives the checks and records results here; this writes them to
//! STUDIO_SMOKE_OUT/results-<instance>.json, asks CI for screenshots through
//! .req/.done files in STUDIO_SMOKE_OUT/shots, makes the server exit with a
//! chosen status (through the preload's trigger file), and measures memory.
//!
//! Environment:
//!   STUDIO_SMOKE=1               turn it on
//!   STUDIO_SMOKE_OUT=DIR         results, screenshots, PIDs
//!   STUDIO_SMOKE_PRELOAD=FILE    test/smoke-preload.mjs
//!   STUDIO_SMOKE_STUB=URL        the stream stub (test/stream-stub.mjs)
//!   STUDIO_SMOKE_FOLDER=PATH     chooseFolder answers this without a dialog
//!   STUDIO_SMOKE_REAL_DIALOG=1   also open the real dialog once (CI cancels it)
//!   STUDIO_SMOKE_SHOTS=0         do not wait for screenshots
//!   STUDIO_SMOKE_IDLE_SECS=N     idle time before the memory reading (10)
//!   STUDIO_SMOKE_TIMEOUT_SECS=N  give up and exit 3 after N seconds (240)
//!   STUDIO_SMOKE_PHASE=R|T|K     R: set by the shell for the relaunched copy;
//!                                T, K: start and wait to be signalled or killed

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde_json::{json, Map, Value};
use tauri::{AppHandle, Manager};

use crate::server;
use crate::Shell;

pub const DIALOG_TITLE: &str = "Smoke test folder dialog";

pub struct Smoke {
    out: PathBuf,
    instance: u32,
    t0: Instant,
    pub preload: Option<PathBuf>,
    pub test_folder: Option<String>,
    stub: String,
    real_dialog: bool,
    shots: bool,
    idle_secs: u64,
    timeout_secs: u64,
    trigger: PathBuf,
    pids: PathBuf,
    phase: Mutex<String>,
    results: Mutex<Map<String, Value>>,
    lists: Mutex<BTreeMap<String, Vec<Value>>>,
    servers: Mutex<Vec<u32>>,
    exit_trigger_at: Mutex<Option<Instant>>,
    failure: Mutex<Option<String>>,
    next_port: Mutex<Option<u16>>,
    finished: AtomicBool,
}

fn ms(d: Duration) -> u64 {
    d.as_millis() as u64
}

impl Smoke {
    pub fn from_env(t0: Instant) -> Option<Smoke> {
        if std::env::var("STUDIO_SMOKE").ok().as_deref() != Some("1") {
            return None;
        }
        let var = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
        let out = PathBuf::from(var("STUDIO_SMOKE_OUT").unwrap_or_else(|| "smoke-out".into()));
        let _ = std::fs::create_dir_all(out.join("shots"));
        // R: the copy a relaunch starts. T and K: copies that only start and
        // wait, for CI to end them with a signal (T: TERM, Linux) or a
        // forced kill (K), to see what outlives the shell.
        let phase = var("STUDIO_SMOKE_PHASE").unwrap_or_else(|| "A".into());
        let instance = match phase.as_str() {
            "R" => 2,
            "T" => 3,
            "K" => 4,
            _ => 1,
        };
        let s = Smoke {
            instance,
            t0,
            preload: var("STUDIO_SMOKE_PRELOAD").map(PathBuf::from),
            test_folder: var("STUDIO_SMOKE_FOLDER"),
            stub: var("STUDIO_SMOKE_STUB").unwrap_or_default(),
            real_dialog: var("STUDIO_SMOKE_REAL_DIALOG").as_deref() == Some("1"),
            shots: var("STUDIO_SMOKE_SHOTS").as_deref() != Some("0"),
            idle_secs: var("STUDIO_SMOKE_IDLE_SECS").and_then(|v| v.parse().ok()).unwrap_or(10),
            timeout_secs: var("STUDIO_SMOKE_TIMEOUT_SECS").and_then(|v| v.parse().ok()).unwrap_or(240),
            trigger: out.join("exit-trigger"),
            pids: out.join("server-pids.jsonl"),
            phase: Mutex::new(phase),
            results: Mutex::new(Map::new()),
            lists: Mutex::new(BTreeMap::new()),
            servers: Mutex::new(Vec::new()),
            exit_trigger_at: Mutex::new(None),
            failure: Mutex::new(None),
            next_port: Mutex::new(None),
            finished: AtomicBool::new(false),
            out,
        };
        s.record("instance", json!(s.instance));
        s.record("shellPid", json!(std::process::id()));
        s.record("os", json!(std::env::consts::OS));
        Some(s)
    }

    fn now_ms(&self) -> u64 {
        ms(self.t0.elapsed())
    }

    fn results_path(&self) -> PathBuf {
        self.out.join(format!("results-{}.json", self.instance))
    }

    pub fn record(&self, key: &str, value: Value) {
        self.results.lock().unwrap().insert(key.to_string(), value);
        self.write();
    }

    fn record_once(&self, key: &str, value: Value) {
        let mut r = self.results.lock().unwrap();
        if !r.contains_key(key) {
            r.insert(key.to_string(), value);
        }
        drop(r);
        self.write();
    }

    pub fn push(&self, list: &str, value: Value) {
        self.lists.lock().unwrap().entry(list.to_string()).or_default().push(value);
        self.write();
    }

    fn list(&self, name: &str) -> Value {
        Value::Array(self.lists.lock().unwrap().get(name).cloned().unwrap_or_default())
    }

    fn write(&self) {
        let mut all = self.results.lock().unwrap().clone();
        for (k, v) in self.lists.lock().unwrap().iter() {
            all.insert(k.clone(), Value::Array(v.clone()));
        }
        all.insert("phase".into(), json!(*self.phase.lock().unwrap()));
        all.insert("writtenAtMs".into(), json!(self.now_ms()));
        let path = self.results_path();
        let tmp = path.with_extension("json.tmp");
        if std::fs::write(&tmp, serde_json::to_vec_pretty(&Value::Object(all)).unwrap_or_default()).is_ok() {
            let _ = std::fs::rename(&tmp, &path);
        }
    }

    pub fn server_env(&self, env: &mut BTreeMap<String, String>) {
        env.insert("STUDIO_SMOKE_PIDS".into(), self.pids.to_string_lossy().into());
        env.insert("STUDIO_SMOKE_TRIGGER".into(), self.trigger.to_string_lossy().into());
    }

    pub fn window_created(&self) {
        self.record("windowCreatedMs", json!(self.now_ms()));
        self.record("webviewVersion", json!(tauri::webview_version().unwrap_or_else(|e| e.to_string())));
    }

    pub fn server_started(&self, pid: u32) {
        self.servers.lock().unwrap().push(pid);
        self.record_once("serverSpawnedMs", json!(self.now_ms()));
    }

    pub fn server_ready(&self, port: u16, startup: Duration) {
        self.record_once("serverAnsweredMs", json!(self.now_ms()));
        self.push("serverStarts", json!({ "port": port, "startupMs": ms(startup), "atMs": self.now_ms() }));
    }

    pub fn server_exited(&self, pid: u32, code: i32) {
        self.push("serverExits", json!({ "pid": pid, "code": code, "atMs": self.now_ms() }));
    }

    pub fn failure_shown(&self, text: &str) {
        *self.failure.lock().unwrap() = Some(text.to_string());
    }

    pub fn stopped(&self, how: &str, took_ms: u128, killed: bool) {
        self.record("stop", json!({ "how": how, "ms": took_ms as u64, "killNeeded": killed, "atMs": self.now_ms() }));
        self.finished.store(true, Ordering::SeqCst);
    }

    pub fn before_relaunch(&self) {
        self.record("relaunchRequestedMs", json!(self.now_ms()));
        // The copy request_restart starts inherits this.
        std::env::set_var("STUDIO_SMOKE_PHASE", "R");
    }

    pub fn download_finished(&self, url: &str, path: Option<&Path>, success: bool) {
        let content = path.and_then(|p| std::fs::read_to_string(p).ok());
        self.push(
            "downloads",
            json!({
                "event": "finished",
                "url": url,
                "path": path.map(|p| p.to_string_lossy().into_owned()),
                "success": success,
                "bytes": content.as_ref().map(|c| c.len()),
                "content": content,
            }),
        );
    }

    /// Asks CI for a screenshot and waits for it (30 s at most).
    fn checkpoint(&self, name: &str) -> bool {
        if !self.shots {
            return false;
        }
        let dir = self.out.join("shots");
        let done = dir.join(format!("{name}-{}.done", self.instance));
        let _ = std::fs::write(dir.join(format!("{name}-{}.req", self.instance)), b"");
        let end = Instant::now() + Duration::from_secs(30);
        while Instant::now() < end {
            if done.exists() {
                return true;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        false
    }

    pub fn checkpoint_soon(&self, name: &'static str) {
        let out = self.out.clone();
        let shots = self.shots;
        let instance = self.instance;
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(1500));
            if shots {
                let _ = std::fs::write(out.join("shots").join(format!("{name}-{instance}.req")), b"");
            }
        });
    }

    /// On the error page: a screenshot, then Try Again pressed in the page.
    pub fn press_retry_soon(&self, app: AppHandle) {
        let shell = app.state::<Arc<Shell>>().inner().clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(1500));
            if let Some(s) = &shell.smoke {
                s.checkpoint("error");
            }
            if let Some(w) = shell.window() {
                let _ = w.eval("document.getElementById('retry') && document.getElementById('retry').click()");
            }
        });
    }

    pub fn start_watchdog(&self, shell: Arc<Shell>) {
        let limit = Duration::from_secs(self.timeout_secs);
        std::thread::spawn(move || {
            std::thread::sleep(limit);
            let Some(s) = &shell.smoke else { return };
            if s.finished.load(Ordering::SeqCst) {
                return;
            }
            s.record("timeout", json!(true));
            let (took, killed) = shell.stop_server();
            s.stopped("timeout", took, killed);
            shell.app.exit(3);
        });
    }

    /// PIDs from earlier servers (and the child each started) still alive.
    fn earlier_alive(&self, current: Option<u32>) -> Vec<u32> {
        let text = std::fs::read_to_string(&self.pids).unwrap_or_default();
        let mut alive = Vec::new();
        for line in text.lines() {
            let Ok(v) = serde_json::from_str::<Value>(line) else { continue };
            let srv = v.get("server").and_then(Value::as_u64).map(|p| p as u32);
            if srv.is_some() && srv == current {
                continue;
            }
            for k in ["server", "child"] {
                if let Some(p) = v.get(k).and_then(Value::as_u64) {
                    if server::pid_alive(p as u32) {
                        alive.push(p as u32);
                    }
                }
            }
        }
        alive
    }

    pub fn handle(&self, shell: &Arc<Shell>, op: &str, data: &Value) -> Result<Value, String> {
        match op {
            "drawn" => {
                let phase = self.phase.lock().unwrap().clone();
                let now = self.now_ms();
                self.record_once("coldStartMs", json!(now));
                let exit_to_drawn = self.exit_trigger_at.lock().unwrap().take().map(|t| ms(t.elapsed()));
                self.push("drawn", json!({ "phase": phase, "atMs": now, "href": data.get("href"), "exitToDrawnMs": exit_to_drawn }));
                let (port, server_pid) = {
                    let st = shell.st.lock().unwrap();
                    (st.port, st.server_pid)
                };
                let servers = self.servers.lock().unwrap().clone();
                let previous = if servers.len() >= 2 { Some(servers[servers.len() - 2]) } else { None };
                let next = {
                    let mut n = self.next_port.lock().unwrap();
                    if phase == "C" && n.is_none() {
                        *n = server::free_port().ok();
                    }
                    *n
                };
                let failure = self.failure.lock().unwrap().clone();
                Ok(json!({
                    "phase": phase,
                    "instance": self.instance,
                    "port": port,
                    "serverPid": server_pid,
                    "previousServerPid": previous,
                    "exitToDrawnMs": exit_to_drawn,
                    "earlierChildrenAlive": self.earlier_alive(server_pid),
                    "sawFailurePage": failure.is_some(),
                    "failureText": failure,
                    "testFolder": self.test_folder,
                    "realDialog": self.real_dialog,
                    "dialogTitle": DIALOG_TITLE,
                    "stub": self.stub,
                    "nextPort": next,
                    "expectedPort": next,
                }))
            }
            "record" => {
                let key = data.get("key").and_then(Value::as_str).ok_or("no key")?;
                self.record(key, data.get("value").cloned().unwrap_or(Value::Null));
                Ok(Value::Null)
            }
            "set_phase" => {
                let p = data.get("phase").and_then(Value::as_str).ok_or("no phase")?;
                *self.phase.lock().unwrap() = p.to_string();
                *self.failure.lock().unwrap() = None;
                self.write();
                Ok(Value::Null)
            }
            "checkpoint" => {
                let name = data.get("name").and_then(Value::as_str).ok_or("no name")?;
                Ok(json!({ "taken": self.checkpoint(name) }))
            }
            "server_exit" => {
                let code = data.get("code").and_then(Value::as_i64).ok_or("no code")?;
                *self.exit_trigger_at.lock().unwrap() = Some(Instant::now());
                std::fs::write(&self.trigger, code.to_string()).map_err(|e| e.to_string())?;
                Ok(Value::Null)
            }
            "downloads" => Ok(self.list("downloads")),
            "opened" => Ok(self.list("opened")),
            "idle_measure" => {
                std::thread::sleep(Duration::from_secs(self.idle_secs));
                let m = memory(shell);
                self.record(&format!("memory{}", self.instance), m.clone());
                Ok(m)
            }
            "finish" => {
                self.write();
                if data.get("close").and_then(Value::as_bool).unwrap_or(false) {
                    if let Some(w) = shell.window() {
                        // As the window's close button does.
                        let _ = w.close();
                    }
                }
                Ok(Value::Null)
            }
            other => Err(format!("unknown smoke op {other}")),
        }
    }
}

struct Proc {
    pid: u32,
    ppid: u32,
    name: String,
    rss: u64,
    /// Start time, in a unit that only orders processes of one boot: a
    /// parent's child never started before it (a reused PID shows as one).
    created: u64,
}

#[cfg(target_os = "linux")]
fn processes() -> Vec<Proc> {
    let mut out = Vec::new();
    let Ok(dir) = std::fs::read_dir("/proc") else { return out };
    for e in dir.flatten() {
        let Ok(pid) = e.file_name().to_string_lossy().parse::<u32>() else { continue };
        let Ok(stat) = std::fs::read_to_string(format!("/proc/{pid}/stat")) else { continue };
        let (Some(open), Some(close)) = (stat.find('('), stat.rfind(')')) else { continue };
        let name = stat[open + 1..close].to_string();
        let ppid = stat[close + 2..].split_whitespace().nth(1).and_then(|v| v.parse().ok()).unwrap_or(0);
        let status = std::fs::read_to_string(format!("/proc/{pid}/status")).unwrap_or_default();
        let rss = status
            .lines()
            .find(|l| l.starts_with("VmRSS:"))
            .and_then(|l| l.split_whitespace().nth(1))
            .and_then(|v| v.parse::<u64>().ok())
            .unwrap_or(0)
            * 1024;
        let created = stat[close + 2..].split_whitespace().nth(19).and_then(|v| v.parse().ok()).unwrap_or(0);
        out.push(Proc { pid, ppid, name, rss, created });
    }
    out
}

#[cfg(windows)]
fn processes() -> Vec<Proc> {
    use std::os::windows::process::CommandExt;
    let out = std::process::Command::new("powershell")
        .args([
            "-NoProfile",
            "-Command",
            "Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,WorkingSetSize,@{n='Created';e={$_.CreationDate.ToFileTimeUtc()}} | ConvertTo-Json -Compress",
        ])
        .creation_flags(0x0800_0000) // CREATE_NO_WINDOW
        .output();
    let Ok(out) = out else { return Vec::new() };
    let v: Value = serde_json::from_slice(&out.stdout).unwrap_or(Value::Null);
    let num = |x: Option<&Value>| -> u64 {
        match x {
            Some(Value::Number(n)) => n.as_u64().unwrap_or(0),
            Some(Value::String(s)) => s.parse().unwrap_or(0),
            _ => 0,
        }
    };
    v.as_array()
        .map(|a| {
            a.iter()
                .map(|p| Proc {
                    pid: num(p.get("ProcessId")) as u32,
                    ppid: num(p.get("ParentProcessId")) as u32,
                    name: p.get("Name").and_then(Value::as_str).unwrap_or("").to_string(),
                    rss: num(p.get("WorkingSetSize")),
                    created: num(p.get("Created")),
                })
                .collect()
        })
        .unwrap_or_default()
}

#[cfg(not(any(target_os = "linux", windows)))]
fn processes() -> Vec<Proc> {
    Vec::new()
}

/// Resident memory (working set on Windows) of the shell process itself,
/// of the web engine processes it started, and of the server's tree.
fn memory(shell: &Shell) -> Value {
    let me = std::process::id();
    let server_pid = shell.st.lock().unwrap().server_pid;
    let procs = processes();
    let children = |root: u32| -> Vec<u32> {
        let mut found = vec![root];
        let mut i = 0;
        while i < found.len() {
            let p = found[i];
            let born = procs.iter().find(|x| x.pid == p).map(|x| x.created).unwrap_or(0);
            for q in &procs {
                if q.ppid == p && q.pid != p && q.created >= born && !found.contains(&q.pid) {
                    found.push(q.pid);
                }
            }
            i += 1;
        }
        found
    };
    let server_tree: Vec<u32> = server_pid.map(children).unwrap_or_default();
    let mut engine: BTreeMap<String, (u32, u64)> = BTreeMap::new();
    for pid in children(me) {
        if pid == me || server_tree.contains(&pid) {
            continue;
        }
        if let Some(p) = procs.iter().find(|p| p.pid == pid) {
            // The process listing itself (Windows runs it through PowerShell), and
            // the default browser the outside-link checks started.
            if matches!(p.name.to_ascii_lowercase().as_str(), "powershell.exe" | "conhost.exe" | "msedge.exe") {
                continue;
            }
            let e = engine.entry(p.name.clone()).or_default();
            e.0 += 1;
            e.1 += p.rss;
        }
    }
    let rss_of = |pid: u32| procs.iter().find(|p| p.pid == pid).map(|p| p.rss).unwrap_or(0);
    let server_rss: u64 = server_tree.iter().map(|p| rss_of(*p)).sum();
    let engine_total: u64 = engine.values().map(|v| v.1).sum();
    json!({
        "shellRssBytes": rss_of(me),
        "engineProcesses": engine.iter().map(|(k, v)| json!({ "name": k, "count": v.0, "rssBytes": v.1 })).collect::<Vec<_>>(),
        "engineRssBytes": engine_total,
        "serverTreeRssBytes": server_rss,
        "serverTreeProcesses": server_tree.len(),
    })
}
