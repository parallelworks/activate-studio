//! ACTIVATE Studio desktop shell (Tauri v2 spike, Linux and Windows).
//!
//! The counterpart of the macOS app's shell (deploy/mac/shell/StudioShell.swift):
//! it starts the Studio's Node server on 127.0.0.1, keeps it running, shows it
//! in a window, and offers the page window.webkit.messageHandlers.studioShell
//! (chooseFolder, studioMoved, relaunch). Downloads go to the Downloads
//! folder; pages outside the Studio open in the default browser.

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod server;
mod smoke;

use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Condvar, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};
use server::{Launch, ServerProcess, Tree};
use tauri::ipc::CapabilityBuilder;
use tauri::webview::{DownloadEvent, NewWindowResponse};
use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_opener::OpenerExt;

const BRIDGE_JS: &str = include_str!("bridge.js");
const SMOKE_JS: &str = include_str!("smoke.js");

/// Restarts on exit 75/76 closer together than this count as a crash loop.
const RESTART_WINDOW: Duration = Duration::from_secs(60);
const RESTARTS_ALLOWED: usize = 5;

enum Cmd {
    Retry,
    Stop,
}

#[derive(Default)]
struct State {
    /// The port the server answers on, while it does.
    port: Option<u16>,
    ready: bool,
    tree: Option<Tree>,
    server_pid: Option<u32>,
    stopping: bool,
    /// The fragment studioMoved asked for, for the next load.
    pending_hash: Option<String>,
    /// Ports the shell started a server on: the Studio's origins.
    allowed: HashSet<u16>,
}

pub struct Shell {
    app: AppHandle,
    data_dir: PathBuf,
    log_dir: PathBuf,
    app_dir: PathBuf,
    node: PathBuf,
    local_url: Mutex<Option<Url>>,
    st: Mutex<State>,
    tx: Mutex<mpsc::Sender<Cmd>>,
    /// Set by the supervisor thread when it has stopped for good.
    done: (Mutex<bool>, Condvar),
    closing: AtomicBool,
    smoke: Option<smoke::Smoke>,
}

fn stamp() -> String {
    let t = SystemTime::now().duration_since(UNIX_EPOCH).unwrap_or_default();
    format!("{}.{:03}", t.as_secs(), t.subsec_millis())
}

impl Shell {
    fn log(&self, line: &str) {
        eprintln!("[shell] {line}");
        server::append_line(&self.log_dir.join("shell.log"), &format!("{} {line}", stamp()));
    }

    fn window(&self) -> Option<WebviewWindow> {
        self.app.get_webview_window("main")
    }

    fn studio_url(port: u16) -> Url {
        Url::parse(&format!("http://127.0.0.1:{port}/")).expect("valid URL")
    }

    /// The Studio on this port may call studio_shell (and, in the smoke
    /// test, smoke): a run-time capability for exactly
    /// http://127.0.0.1:<port>/*, in the main window only.
    fn allow_port(&self, port: u16) {
        if !self.st.lock().unwrap().allowed.insert(port) {
            return;
        }
        let mut cap = CapabilityBuilder::new(format!("studio-{port}"))
            .remote(format!("http://127.0.0.1:{port}"))
            .local(false)
            .window("main")
            .permission("allow-studio-shell");
        if self.smoke.is_some() {
            cap = cap.permission("allow-smoke");
        }
        match self.app.add_capability(cap) {
            Ok(()) => self.log(&format!("IPC allowed for http://127.0.0.1:{port}")),
            Err(e) => self.log(&format!("could not add the capability for port {port}: {e}")),
        }
    }

    /// Navigations that stay in the window: the Studio's own origins, the
    /// shell's start page, blobs and data. Anything else opens in the
    /// default browser and the window stays where it is.
    fn allow_navigation(&self, url: &Url) -> bool {
        match url.scheme() {
            "about" | "blob" | "data" | "tauri" => return true,
            _ => {}
        }
        if url.host_str() == Some("tauri.localhost") {
            return true;
        }
        if url.scheme() == "http" && url.host_str() == Some("127.0.0.1") {
            if let Some(port) = url.port() {
                if self.st.lock().unwrap().allowed.contains(&port) {
                    return true;
                }
            }
        }
        self.open_outside(url);
        false
    }

    fn open_outside(&self, url: &Url) {
        if !matches!(url.scheme(), "http" | "https" | "mailto") {
            self.log(&format!("not opening {url}: not a web link"));
            return;
        }
        let r = self.app.opener().open_url(url.as_str(), None::<&str>);
        self.log(&format!("opening {url} in the default browser: {r:?}"));
        if let Some(s) = &self.smoke {
            s.push("opened", json!({ "url": url.as_str(), "ok": r.is_ok(), "error": r.err().map(|e| e.to_string()) }));
        }
    }

    fn navigate(&self, url: Url) {
        if let Some(w) = self.window() {
            self.log(&format!("loading {url}"));
            if let Err(e) = w.navigate(url) {
                self.log(&format!("navigate failed: {e}"));
            }
        }
    }

    /// The start page, saying the server could not start or stopped, with
    /// Try Again and Open Logs.
    fn show_failure(&self, title: &str, detail: &str) {
        let Some(mut url) = self.local_url.lock().unwrap().clone() else { return };
        let tail = server::log_tail(&self.log_dir.join("studio.log"), 12);
        let state = json!({ "title": title, "detail": detail, "log": tail }).to_string();
        url.set_fragment(Some(&state));
        if let Some(s) = &self.smoke {
            s.failure_shown(&format!("{title} {detail}"));
        }
        self.navigate(url);
    }

    /// Where the window goes once the server answers on `port`: the page
    /// it showed, moved to the new port (after a restart), the fragment
    /// studioMoved asked for, or the Studio's start.
    fn studio_target(&self, port: u16) -> Url {
        let mut target = Self::studio_url(port);
        let hash = self.st.lock().unwrap().pending_hash.take();
        if let Some(h) = hash {
            target.set_fragment(Some(h.trim_start_matches('#')));
            return target;
        }
        if let Some(cur) = self.window().and_then(|w| w.url().ok()) {
            if cur.scheme() == "http" && cur.host_str() == Some("127.0.0.1") {
                let mut u = cur.clone();
                let _ = u.set_port(Some(port));
                return u;
            }
        }
        target
    }

    /// The server's environment and port: this process's environment, the
    /// shell's defaults, the settings file the server writes (studio.env,
    /// as deploy/mac/studio-run.sh reads it), then what is not a setting.
    fn launch(&self) -> (Launch, u16) {
        let config = self.data_dir.join("studio.env");
        let settings = server::read_settings(&config);
        let mut env: BTreeMap<String, String> = std::env::vars().collect();
        env.insert("NODE_ENV".into(), "production".into());
        env.insert("INDEX_BASE".into(), self.data_dir.join("index").to_string_lossy().into());
        let kb_chosen = settings.contains_key("KB_ROOT") || env.contains_key("KB_ROOT");
        for (k, v) in &settings {
            env.insert(k.clone(), v.clone());
        }
        if kb_chosen {
            // A folder the person chose holds their own material: no starter folders.
            env.entry("KB_STARTER_DIRS".into()).or_default();
        } else {
            let docs = self.app.path().document_dir().unwrap_or_else(|_| self.data_dir.clone());
            let kb = docs.join("ACTIVATE Studio");
            let _ = std::fs::create_dir_all(&kb);
            env.insert("KB_ROOT".into(), kb.to_string_lossy().into());
        }
        // The saved port when it is free; otherwise any free port.
        let saved = settings.get("PORT").and_then(|p| p.parse::<u16>().ok()).filter(|p| *p >= 1024);
        let port = match saved {
            Some(p) if server::port_free(p) => p,
            _ => server::free_port().expect("no free port on 127.0.0.1"),
        };
        let fixed = [
            ("PORT", port.to_string()),
            ("HOST", "127.0.0.1".to_string()),
            ("STUDIO_CONFIG_FILE", config.to_string_lossy().into()),
            ("STUDIO_RESTARTABLE", "1".to_string()),
            // An update restarts the server through this shell (exit 76).
            ("STUDIO_SUPERVISOR", "detached".to_string()),
            ("STUDIO_DATA_DIR", self.data_dir.to_string_lossy().into()),
        ];
        for (k, v) in fixed {
            env.insert(k.into(), v);
        }
        let main_js = self.app_dir.join("server").join("dist").join("main.js");
        let mut args = Vec::new();
        if let Some(s) = &self.smoke {
            s.server_env(&mut env);
            if let Some(pre) = &s.preload {
                args.push("--import".to_string());
                args.push(server::file_url(pre));
            }
        }
        args.push(main_js.to_string_lossy().into());
        let launch = Launch {
            program: self.node.clone(),
            args,
            env,
            cwd: self.data_dir.clone(),
            log: self.log_dir.join("studio.log"),
        };
        (launch, port)
    }

    /// Blocks until Try Again (true) or the shell stops (false).
    fn wait_for_retry(&self, rx: &mpsc::Receiver<Cmd>) -> bool {
        if let Some(s) = &self.smoke {
            s.press_retry_soon(self.app.clone());
        }
        loop {
            match rx.recv() {
                Ok(Cmd::Retry) => return !self.st.lock().unwrap().stopping,
                Ok(Cmd::Stop) | Err(_) => return false,
            }
        }
    }

    /// The supervisor: one thread for the life of the app (on Linux the
    /// server's PR_SET_PDEATHSIG follows this thread).
    fn supervise(self: Arc<Self>, rx: mpsc::Receiver<Cmd>) {
        let mut restarts: Vec<Instant> = Vec::new();
        loop {
            if self.st.lock().unwrap().stopping {
                break;
            }
            // A Try Again pressed twice is spent.
            while let Ok(Cmd::Retry) = rx.try_recv() {}
            let (launch, port) = self.launch();
            self.log(&format!("starting {} {:?} on port {port}", launch.program.display(), launch.args));
            let mut proc = match ServerProcess::spawn(&launch) {
                Ok(p) => p,
                Err(e) => {
                    self.log(&format!("could not start the server: {e}"));
                    self.show_failure(
                        "ACTIVATE Studio could not start.",
                        &format!("{} could not be run: {e}", launch.program.display()),
                    );
                    if self.wait_for_retry(&rx) {
                        continue;
                    }
                    break;
                }
            };
            let pid = proc.pid();
            let stopping = {
                let mut st = self.st.lock().unwrap();
                st.tree = Some(proc.tree());
                st.server_pid = Some(pid);
                st.stopping
            };
            if stopping {
                // The window closed while this one was being started.
                proc.tree().kill();
                let _ = proc.wait();
                break;
            }
            if let Some(s) = &self.smoke {
                s.server_started(pid);
            }
            let started = Instant::now();
            let ready = server::wait_for_version(port, Duration::from_secs(90), || {
                matches!(proc.try_wait(), Ok(None))
            });
            match ready {
                Ok(version) => {
                    self.log(&format!(
                        "server {pid} answered on {port} after {} ms: {}",
                        started.elapsed().as_millis(),
                        version.trim()
                    ));
                    self.allow_port(port);
                    {
                        let mut st = self.st.lock().unwrap();
                        st.port = Some(port);
                        st.ready = true;
                    }
                    if let Some(s) = &self.smoke {
                        s.server_ready(port, started.elapsed());
                    }
                    let target = self.studio_target(port);
                    self.navigate(target);
                }
                Err(why) => {
                    proc.tree().kill();
                    let _ = proc.wait();
                    let stopping = {
                        let mut st = self.st.lock().unwrap();
                        st.tree = None;
                        st.stopping
                    };
                    if stopping {
                        break;
                    }
                    self.log(&format!("server did not start: {why}"));
                    self.show_failure("ACTIVATE Studio did not start.", &why);
                    if self.wait_for_retry(&rx) {
                        continue;
                    }
                    break;
                }
            }
            let status = proc.wait();
            let code = status.as_ref().map(server::exit_code).unwrap_or(-1);
            let stopping = {
                let mut st = self.st.lock().unwrap();
                st.ready = false;
                st.tree = None;
                st.port = None;
                st.stopping
            };
            if stopping {
                break;
            }
            self.log(&format!("server {pid} exited with status {code}"));
            if let Some(s) = &self.smoke {
                s.server_exited(pid, code);
            }
            if code == server::RESTART_EXIT_CODE || code == server::RELAUNCH_EXIT_CODE {
                let now = Instant::now();
                restarts.retain(|t| now.duration_since(*t) < RESTART_WINDOW);
                restarts.push(now);
                if restarts.len() <= RESTARTS_ALLOWED {
                    // Started again at once; the window keeps its page until
                    // the server answers, then reloads it.
                    continue;
                }
                self.log("the server asked to restart too often");
            }
            self.show_failure("ACTIVATE Studio stopped.", &format!("Its server stopped with status {code}."));
            if !self.wait_for_retry(&rx) {
                break;
            }
        }
        self.log("supervisor stopped");
        let (lock, cv) = &self.done;
        *lock.lock().unwrap() = true;
        cv.notify_all();
    }

    /// Stops the server and everything it started: TERM to the process
    /// group (Linux) or the end of its Job Object (Windows), KILL after
    /// 10 s. Returns (milliseconds taken, whether KILL was needed).
    fn stop_server(&self) -> (u128, bool) {
        let t = Instant::now();
        let tree = {
            let mut st = self.st.lock().unwrap();
            st.stopping = true;
            st.tree.clone()
        };
        let _ = self.tx.lock().unwrap().send(Cmd::Stop);
        if let Some(tree) = &tree {
            tree.terminate();
        }
        let (lock, cv) = &self.done;
        let guard = lock.lock().unwrap();
        let (guard, res) = cv.wait_timeout_while(guard, Duration::from_secs(10), |d| !*d).unwrap();
        drop(guard);
        let mut killed = false;
        if res.timed_out() {
            if let Some(tree) = &tree {
                tree.kill();
            }
            killed = true;
            let guard = lock.lock().unwrap();
            let _ = cv.wait_timeout_while(guard, Duration::from_secs(3), |d| !*d);
        } else if let Some(tree) = &tree {
            // The server is gone; anything left in its group or job goes too.
            tree.kill();
        }
        (t.elapsed().as_millis(), killed)
    }

    /// The window was closed: stop the server, then exit.
    fn close(self: &Arc<Self>) {
        if self.closing.swap(true, Ordering::SeqCst) {
            return;
        }
        let me = self.clone();
        std::thread::spawn(move || {
            let (ms, killed) = me.stop_server();
            me.log(&format!("server stopped in {ms} ms (kill needed: {killed}); exiting"));
            if let Some(s) = &me.smoke {
                s.stopped("window-close", ms, killed);
            }
            me.app.exit(0);
        });
    }

    fn choose_folder(&self, msg: &Value, reply: mpsc::Sender<Option<String>>) {
        let str_of = |k: &str| msg.get(k).and_then(Value::as_str).map(str::to_owned);
        let real = msg.get("smokeRealDialog").and_then(Value::as_bool).unwrap_or(false);
        if let Some(fixed) = self.smoke.as_ref().and_then(|s| s.test_folder.clone()) {
            if !real {
                let _ = reply.send(Some(fixed));
                return;
            }
        }
        let title = str_of("message").unwrap_or_else(|| "Choose a folder.".into());
        let mut b = self.app.dialog().file().set_title(&title);
        if let Some(w) = self.window() {
            b = b.set_parent(&w);
        }
        if let Some(start) = str_of("start").filter(|s| !s.is_empty()) {
            let home = self.app.path().home_dir().unwrap_or_default();
            let p = if start == "~" {
                home
            } else if let Some(rest) = start.strip_prefix("~/") {
                home.join(rest)
            } else {
                PathBuf::from(start)
            };
            b = b.set_directory(p);
        }
        // The dialog has no button label setting (rfd), so `prompt` is not used.
        self.log(&format!("opening the folder dialog: {title}"));
        b.pick_folder(move |picked| {
            let path = picked.and_then(|p| p.into_path().ok()).map(|p| p.to_string_lossy().into_owned());
            let _ = reply.send(path);
        });
        if real {
            if let Some(s) = &self.smoke {
                s.checkpoint_soon("dialog");
            }
        }
    }
}

/// window.webkit.messageHandlers.studioShell.postMessage({action, ...}).
#[tauri::command]
async fn studio_shell(
    shell: tauri::State<'_, Arc<Shell>>,
    webview: tauri::Webview,
    msg: Value,
) -> Result<Value, String> {
    if webview.label() != "main" {
        return Err("not available here".into());
    }
    let action = msg.get("action").and_then(Value::as_str).unwrap_or("");
    match action {
        "chooseFolder" => {
            let (tx, rx) = mpsc::channel();
            shell.choose_folder(&msg, tx);
            let picked = tauri::async_runtime::spawn_blocking(move || rx.recv().ok().flatten())
                .await
                .map_err(|e| e.to_string())?;
            Ok(picked.map(Value::String).unwrap_or(Value::Null))
        }
        "studioMoved" => {
            let port = msg.get("port").and_then(Value::as_u64).filter(|p| (1024..=65535).contains(p));
            let Some(port) = port else { return Err("not a port".into()) };
            let port = port as u16;
            let hash = msg.get("hash").and_then(Value::as_str).filter(|h| h.starts_with('#')).map(str::to_owned);
            shell.log(&format!("studioMoved to {port} {hash:?}"));
            let now_ready = {
                let mut st = shell.st.lock().unwrap();
                st.pending_hash = hash;
                st.ready && st.port == Some(port)
            };
            // The server restarting on that port loads it when it answers;
            // one that answers there already is loaded now.
            if now_ready {
                let target = shell.studio_target(port);
                shell.navigate(target);
            }
            Ok(Value::Bool(true))
        }
        "relaunch" => {
            shell.log("relaunch requested");
            if let Some(s) = &shell.smoke {
                s.before_relaunch();
            }
            let app = shell.app.clone();
            std::thread::spawn(move || {
                // After the reply reaches the page.
                std::thread::sleep(Duration::from_millis(300));
                app.request_restart();
            });
            Ok(Value::Bool(true))
        }
        other => Err(format!("unknown request {other}")),
    }
}

/// Try Again, on the start page.
#[tauri::command]
fn retry(shell: tauri::State<'_, Arc<Shell>>) {
    shell.log("try again");
    let _ = shell.tx.lock().unwrap().send(Cmd::Retry);
}

/// Open Logs, on the start page.
#[tauri::command]
fn open_logs(shell: tauri::State<'_, Arc<Shell>>) -> Result<(), String> {
    shell
        .app
        .opener()
        .open_path(shell.log_dir.to_string_lossy(), None::<&str>)
        .map_err(|e| e.to_string())
}

/// The smoke test's own requests (granted only with STUDIO_SMOKE=1).
#[tauri::command]
async fn smoke(shell: tauri::State<'_, Arc<Shell>>, op: String, data: Value) -> Result<Value, String> {
    let shell: Arc<Shell> = shell.inner().clone();
    tauri::async_runtime::spawn_blocking(move || match &shell.smoke {
        Some(s) => s.handle(&shell, &op, &data),
        None => Err("not in smoke-test mode".into()),
    })
    .await
    .map_err(|e| e.to_string())?
}

/// The Node to run: STUDIO_NODE, else a node beside this executable (where
/// a sidecar is installed), else `node` on the PATH.
fn find_node() -> PathBuf {
    if let Some(p) = std::env::var_os("STUDIO_NODE") {
        return PathBuf::from(p);
    }
    let name = if cfg!(windows) { "node.exe" } else { "node" };
    if let Some(dir) = std::env::current_exe().ok().and_then(|e| e.parent().map(Path::to_path_buf)) {
        let beside = dir.join(name);
        if beside.is_file() {
            return beside;
        }
    }
    PathBuf::from(name)
}

/// TERM, INT and HUP end the app the orderly way (RunEvent::Exit stops
/// the server and its process group); without this a TERM would end the
/// shell at once, and only the server, through PR_SET_PDEATHSIG, would
/// hear of it. A signal handler can only write to a pipe; a thread reads it.
#[cfg(unix)]
fn stop_on_signals(app: AppHandle) {
    use std::io::Read;
    use std::os::unix::io::FromRawFd;
    use std::sync::atomic::AtomicI32;
    static WRITE_FD: AtomicI32 = AtomicI32::new(-1);
    extern "C" fn on_signal(_: libc::c_int) {
        let fd = WRITE_FD.load(Ordering::Relaxed);
        let b = 1u8;
        unsafe {
            libc::write(fd, &b as *const u8 as *const libc::c_void, 1);
        }
    }
    let mut fds = [0 as libc::c_int; 2];
    if unsafe { libc::pipe(fds.as_mut_ptr()) } != 0 {
        return;
    }
    unsafe {
        libc::fcntl(fds[0], libc::F_SETFD, libc::FD_CLOEXEC);
        libc::fcntl(fds[1], libc::F_SETFD, libc::FD_CLOEXEC);
    }
    WRITE_FD.store(fds[1], Ordering::Relaxed);
    for sig in [libc::SIGTERM, libc::SIGINT, libc::SIGHUP] {
        unsafe {
            libc::signal(sig, on_signal as extern "C" fn(libc::c_int) as libc::sighandler_t);
        }
    }
    std::thread::spawn(move || {
        let mut f = unsafe { std::fs::File::from_raw_fd(fds[0]) };
        let mut b = [0u8; 1];
        if f.read(&mut b).is_ok() {
            app.exit(0);
        }
    });
}

fn main() {
    let t0 = Instant::now();
    let smoke_mode = smoke::Smoke::from_env(t0);
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(
            // The plugin's own script turns clicks on target=_blank links into
            // plugin:opener|open_url calls, which the Studio's origin may not
            // make (and the click is swallowed). With it off, the click reaches
            // the new-window hook below, which opens the default browser.
            tauri_plugin_opener::Builder::new().open_js_links_on_click(false).build(),
        )
        .invoke_handler(tauri::generate_handler![studio_shell, retry, open_logs, smoke])
        .setup(move |app| {
            let handle = app.handle().clone();
            let data_dir = match std::env::var_os("STUDIO_DATA_DIR") {
                Some(d) => PathBuf::from(d),
                None => handle.path().app_local_data_dir()?,
            };
            let log_dir = data_dir.join("logs");
            std::fs::create_dir_all(data_dir.join("index"))?;
            std::fs::create_dir_all(&log_dir)?;
            let app_dir = match std::env::var_os("STUDIO_APP_DIR") {
                Some(d) => PathBuf::from(d),
                None => handle.path().resource_dir()?.join("app"),
            };
            let (tx, rx) = mpsc::channel();
            let shell = Arc::new(Shell {
                app: handle.clone(),
                data_dir,
                log_dir,
                app_dir,
                node: find_node(),
                local_url: Mutex::new(None),
                st: Mutex::new(State::default()),
                tx: Mutex::new(tx),
                done: (Mutex::new(false), Condvar::new()),
                closing: AtomicBool::new(false),
                smoke: smoke_mode,
            });
            app.manage(shell.clone());
            shell.log(&format!("shell {} starting, data in {}", std::process::id(), shell.data_dir.display()));

            let nav = shell.clone();
            let popup = shell.clone();
            let dl = shell.clone();
            let mut builder = WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                .title("ACTIVATE Studio")
                .inner_size(1280.0, 800.0)
                .min_inner_size(800.0, 560.0)
                .initialization_script(BRIDGE_JS)
                .on_navigation(move |url| nav.allow_navigation(url))
                .on_new_window(move |url, _features| {
                    // window.open and target=_blank: outside the Studio, the
                    // default browser; the Studio's own pages, a plain window.
                    if popup.allow_navigation(&url) && url.scheme() == "http" {
                        NewWindowResponse::Allow
                    } else {
                        NewWindowResponse::Deny
                    }
                })
                .on_download(move |_webview, event| {
                    match event {
                        DownloadEvent::Requested { url, destination } => {
                            // wry proposes the system's Downloads folder (or the
                            // working directory when there is none) and a name
                            // that does not overwrite. The hook may change the
                            // path or refuse the download (return false); here it
                            // is always the Downloads folder, as on macOS.
                            let name = destination
                                .file_name()
                                .map(|n| n.to_string_lossy().into_owned())
                                .unwrap_or_else(|| "download".into());
                            let dir = server::downloads_dir(
                                dl.app.path().download_dir().ok(),
                                dl.app.path().home_dir().ok(),
                            );
                            if let Some(dir) = dir {
                                *destination = server::unique_in(&dir, &name);
                            }
                            dl.log(&format!("download of {url} to {}", destination.display()));
                            if let Some(s) = &dl.smoke {
                                s.push(
                                    "downloads",
                                    json!({ "event": "requested", "url": url.as_str(), "destination": destination.to_string_lossy() }),
                                );
                            }
                        }
                        DownloadEvent::Finished { url, path, success } => {
                            dl.log(&format!("download of {url} finished: {success} {path:?}"));
                            if let Some(s) = &dl.smoke {
                                s.download_finished(url.as_str(), path.as_deref(), success);
                            }
                        }
                        #[allow(unreachable_patterns)]
                        _ => {}
                    }
                    true
                });
            if shell.smoke.is_some() {
                builder = builder.initialization_script(SMOKE_JS);
            }
            let window = builder.build()?;
            #[cfg(target_os = "linux")]
            if shell.smoke.is_some() {
                // The smoke test's window.open and target=_blank clicks come
                // from script, not a person's click, which WebKitGTK blocks
                // as popups before the new-window hook sees them.
                let _ = window.with_webview(|wv| {
                    use webkit2gtk::{SettingsExt, WebViewExt};
                    if let Some(settings) = wv.inner().settings() {
                        settings.set_javascript_can_open_windows_automatically(true);
                    }
                });
            }
            // The start page's address. window.url() cannot be used here: on
            // Windows it still reads about:blank until the first load commits,
            // which made the error page a blank page without its buttons.
            let local = if cfg!(windows) { "http://tauri.localhost/index.html" } else { "tauri://localhost/index.html" };
            *shell.local_url.lock().unwrap() = Url::parse(local).ok();
            let on_close = shell.clone();
            window.on_window_event(move |e| {
                if let WindowEvent::CloseRequested { api, .. } = e {
                    api.prevent_close();
                    on_close.close();
                }
            });
            if let Some(s) = &shell.smoke {
                s.window_created();
                s.start_watchdog(shell.clone());
            }
            #[cfg(unix)]
            stop_on_signals(handle.clone());
            let sup = shell.clone();
            std::thread::Builder::new().name("supervisor".into()).spawn(move || sup.supervise(rx))?;
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("could not build the app");

    app.run(|app, event| {
        if let RunEvent::Exit = event {
            // Quit any way other than the window's close (a relaunch): the
            // server still stops first.
            if let Some(shell) = app.try_state::<Arc<Shell>>() {
                if !*shell.done.0.lock().unwrap() {
                    let (ms, killed) = shell.stop_server();
                    shell.log(&format!("server stopped at exit in {ms} ms (kill needed: {killed})"));
                    if let Some(s) = &shell.smoke {
                        s.stopped("exit", ms, killed);
                    }
                }
            }
        }
    });
}
