//! Starting, watching and stopping the Studio's Node server.
//!
//! Plain std, plus libc on Unix and windows-sys on Windows, with no Tauri
//! types, so this file can be read and tested on its own.
//!
//! Every server start gets its own process group (Unix) or Job Object
//! (Windows), so the server and everything it starts (indexers, CLIs) can be
//! stopped together, and nothing it started outlives it.

use std::collections::BTreeMap;
use std::fs::OpenOptions;
use std::io::{self, Read, Write};
use std::net::{Ipv4Addr, SocketAddr, TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitStatus, Stdio};
#[cfg(not(unix))]
use std::sync::Arc;
use std::time::{Duration, Instant};

/// Exit status with which the server asks to be started again, after a
/// setting read only at startup (the port, the folder) changed.
pub const RESTART_EXIT_CODE: i32 = 75;
/// Exit status the server uses after an update replaced the app.
pub const RELAUNCH_EXIT_CODE: i32 = 76;

/// What to run, and how.
pub struct Launch {
    pub program: PathBuf,
    pub args: Vec<String>,
    /// The complete environment of the server (nothing else is inherited).
    pub env: BTreeMap<String, String>,
    pub cwd: PathBuf,
    /// The server's stdout and stderr are appended here.
    pub log: PathBuf,
}

/// A free TCP port on 127.0.0.1, found by binding port 0. Another program
/// could take it before the server binds it; the server then fails to start
/// and the shell shows the error.
pub fn free_port() -> io::Result<u16> {
    let l = TcpListener::bind((Ipv4Addr::LOCALHOST, 0))?;
    Ok(l.local_addr()?.port())
}

/// Whether nothing listens on 127.0.0.1:port now.
pub fn port_free(port: u16) -> bool {
    TcpListener::bind((Ipv4Addr::LOCALHOST, port)).is_ok()
}

/// The settings file the server writes for its next start (studio.env:
/// KEY=value lines, values single-quoted for a POSIX shell; see
/// server/src/lifecycle.ts). Missing file: no settings.
pub fn read_settings(path: &Path) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    let Ok(text) = std::fs::read_to_string(path) else { return out };
    for line in text.lines() {
        let line = line.trim();
        let Some((key, value)) = line.split_once('=') else { continue };
        let key_ok = key.chars().next().is_some_and(|c| c.is_ascii_uppercase())
            && key.chars().all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_');
        if !key_ok {
            continue;
        }
        out.insert(key.to_string(), unquote(value));
    }
    out
}

/// 'value' with '\'' for each single quote inside, as lifecycle.ts writes it.
fn unquote(v: &str) -> String {
    if v.len() >= 2 && v.starts_with('\'') && v.ends_with('\'') {
        v[1..v.len() - 1].replace("'\\''", "'")
    } else {
        v.to_string()
    }
}

/// GET http://127.0.0.1:port/path with HTTP/1.0 (so the server closes the
/// connection after the body). Returns the status and the body.
pub fn http_get(port: u16, path: &str, timeout: Duration) -> io::Result<(u16, String)> {
    let addr = SocketAddr::from((Ipv4Addr::LOCALHOST, port));
    let mut s = TcpStream::connect_timeout(&addr, timeout)?;
    s.set_read_timeout(Some(timeout))?;
    s.set_write_timeout(Some(timeout))?;
    write!(s, "GET {path} HTTP/1.0\r\nHost: 127.0.0.1:{port}\r\nAccept: application/json\r\n\r\n")?;
    let mut raw = Vec::new();
    s.read_to_end(&mut raw)?;
    let text = String::from_utf8_lossy(&raw).into_owned();
    let (head, body) = text.split_once("\r\n\r\n").unwrap_or((&text, ""));
    let status = head
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse().ok())
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "not an HTTP response"))?;
    Ok((status, body.to_string()))
}

/// Polls GET /api/version until it answers 200 (returns the body), the
/// server exits (`alive` returns false), or the time is up.
pub fn wait_for_version(port: u16, limit: Duration, mut alive: impl FnMut() -> bool) -> Result<String, String> {
    let start = Instant::now();
    loop {
        if let Ok((200, body)) = http_get(port, "/api/version", Duration::from_millis(1000)) {
            return Ok(body);
        }
        if !alive() {
            return Err("the server stopped before it answered".into());
        }
        if start.elapsed() > limit {
            return Err(format!("the server did not answer on port {port} within {}s", limit.as_secs()));
        }
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The last `n` lines of a log file, for an error message.
pub fn log_tail(path: &Path, n: usize) -> String {
    let text = std::fs::read_to_string(path).unwrap_or_default();
    let lines: Vec<&str> = text.lines().collect();
    lines[lines.len().saturating_sub(n)..].join("\n")
}

/// A running server. Owned by the thread that waits on it.
pub struct ServerProcess {
    child: Child,
    tree: Tree,
}

/// Stops a server's whole process tree, from any thread.
#[derive(Clone)]
pub struct Tree {
    #[cfg(unix)]
    pgid: i32,
    #[cfg(windows)]
    job: Arc<windows_job::Job>,
    #[cfg(not(any(unix, windows)))]
    _none: Arc<()>,
}

impl ServerProcess {
    pub fn spawn(l: &Launch) -> io::Result<ServerProcess> {
        if let Some(dir) = l.log.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let log = OpenOptions::new().create(true).append(true).open(&l.log)?;
        let mut cmd = Command::new(&l.program);
        cmd.args(&l.args)
            .env_clear()
            .envs(&l.env)
            .current_dir(&l.cwd)
            .stdin(Stdio::null())
            .stdout(Stdio::from(log.try_clone()?))
            .stderr(Stdio::from(log));
        platform_prepare(&mut cmd);
        let child = cmd.spawn()?;
        let tree = platform_attach(&child)?;
        Ok(ServerProcess { child, tree })
    }

    pub fn pid(&self) -> u32 {
        self.child.id()
    }

    pub fn tree(&self) -> Tree {
        self.tree.clone()
    }

    pub fn try_wait(&mut self) -> io::Result<Option<ExitStatus>> {
        self.child.try_wait()
    }

    /// Waits for the server to exit, then stops anything it left running.
    pub fn wait(&mut self) -> io::Result<ExitStatus> {
        let status = self.child.wait()?;
        self.tree.kill();
        Ok(status)
    }
}

/// The exit code, or 128 + the signal on Unix when a signal ended it.
pub fn exit_code(status: &ExitStatus) -> i32 {
    if let Some(c) = status.code() {
        return c;
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(sig) = status.signal() {
            return 128 + sig;
        }
    }
    -1
}

// ---- Unix: a process group per server ----

#[cfg(unix)]
fn platform_prepare(cmd: &mut Command) {
    use std::os::unix::process::CommandExt;
    // Its own process group (pgid = its pid), so one kill(-pgid) reaches the
    // server and everything it started that did not leave the group.
    cmd.process_group(0);
    #[cfg(target_os = "linux")]
    {
        // If the thread that started the server goes away (the shell
        // crashed or was killed), the kernel sends the server SIGTERM. The
        // signal follows the parent *thread*, so servers are started from
        // one supervisor thread that lives as long as the app.
        let parent = std::process::id() as libc::pid_t;
        unsafe {
            cmd.pre_exec(move || {
                if libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGTERM) != 0 {
                    return Err(io::Error::last_os_error());
                }
                // The parent may have died between fork and prctl.
                if libc::getppid() != parent {
                    libc::_exit(1);
                }
                Ok(())
            });
        }
    }
}

#[cfg(unix)]
fn platform_attach(child: &Child) -> io::Result<Tree> {
    Ok(Tree { pgid: child.id() as i32 })
}

#[cfg(unix)]
impl Tree {
    /// Asks the whole group to stop (SIGTERM; the server drains and exits).
    pub fn terminate(&self) {
        unsafe {
            libc::kill(-self.pgid, libc::SIGTERM);
        }
    }
    /// Stops the whole group at once (SIGKILL). Harmless when it is gone.
    pub fn kill(&self) {
        unsafe {
            libc::kill(-self.pgid, libc::SIGKILL);
        }
    }
}

// ---- Windows: a Job Object per server ----

#[cfg(windows)]
fn platform_prepare(cmd: &mut Command) {
    use std::os::windows::process::CommandExt;
    // node.exe is a console program; without this a console window opens
    // beside the app's window.
    cmd.creation_flags(windows_sys::Win32::System::Threading::CREATE_NO_WINDOW);
}

#[cfg(windows)]
fn platform_attach(child: &Child) -> io::Result<Tree> {
    let job = windows_job::Job::new()?;
    // Processes the server starts from now on are in the job too. Node
    // takes tens of milliseconds to start anything, so assigning right after
    // the spawn (rather than starting it suspended) is enough in practice.
    job.assign(child)?;
    Ok(Tree { job: Arc::new(job) })
}

#[cfg(windows)]
impl Tree {
    /// Windows has no SIGTERM for a windowless console program, so a stop
    /// ends every process in the job at once (the server's drain does not
    /// run; see the spike report).
    pub fn terminate(&self) {
        self.job.terminate(1);
    }
    pub fn kill(&self) {
        self.job.terminate(1);
    }
}

#[cfg(windows)]
mod windows_job {
    use std::io;
    use std::os::windows::io::AsRawHandle;
    use std::process::Child;
    use windows_sys::Win32::Foundation::{CloseHandle, HANDLE};
    use windows_sys::Win32::System::JobObjects::{
        AssignProcessToJobObject, CreateJobObjectW, JobObjectExtendedLimitInformation,
        SetInformationJobObject, TerminateJobObject, JOBOBJECT_EXTENDED_LIMIT_INFORMATION,
        JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
    };

    /// A Job Object that kills every process in it when its last handle
    /// closes: when the shell drops it, and when the shell process dies for
    /// any reason (the OS closes the handle).
    pub struct Job(HANDLE);
    // The handle is used only through thread-safe Win32 calls.
    unsafe impl Send for Job {}
    unsafe impl Sync for Job {}

    impl Job {
        pub fn new() -> io::Result<Job> {
            unsafe {
                let h = CreateJobObjectW(std::ptr::null(), std::ptr::null());
                if h.is_null() {
                    return Err(io::Error::last_os_error());
                }
                let job = Job(h);
                let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
                info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
                let ok = SetInformationJobObject(
                    job.0,
                    JobObjectExtendedLimitInformation,
                    &info as *const _ as *const core::ffi::c_void,
                    std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
                );
                if ok == 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(job)
            }
        }

        pub fn assign(&self, child: &Child) -> io::Result<()> {
            let ok = unsafe { AssignProcessToJobObject(self.0, child.as_raw_handle() as HANDLE) };
            if ok == 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        }

        pub fn terminate(&self, code: u32) {
            unsafe {
                TerminateJobObject(self.0, code);
            }
        }
    }

    impl Drop for Job {
        fn drop(&mut self) {
            unsafe {
                CloseHandle(self.0);
            }
        }
    }
}

#[cfg(not(any(unix, windows)))]
fn platform_prepare(_cmd: &mut Command) {}
#[cfg(not(any(unix, windows)))]
fn platform_attach(_child: &Child) -> io::Result<Tree> {
    Ok(Tree { _none: Arc::new(()) })
}
#[cfg(not(any(unix, windows)))]
impl Tree {
    pub fn terminate(&self) {}
    pub fn kill(&self) {}
}

/// True while a process with this PID exists (Unix: kill 0; Windows:
/// OpenProcess and GetExitCodeProcess). For the shell's own checks.
pub fn pid_alive(pid: u32) -> bool {
    #[cfg(unix)]
    unsafe {
        libc::kill(pid as libc::pid_t, 0) == 0
    }
    #[cfg(windows)]
    unsafe {
        use windows_sys::Win32::Foundation::{CloseHandle, STILL_ACTIVE};
        use windows_sys::Win32::System::Threading::{
            GetExitCodeProcess, OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION,
        };
        let h = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid);
        if h.is_null() {
            return false;
        }
        let mut code: u32 = 0;
        let ok = GetExitCodeProcess(h, &mut code);
        CloseHandle(h);
        ok != 0 && code == STILL_ACTIVE as u32
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = pid;
        false
    }
}

/// Appends a line to a file, for the shell's own log.
pub fn append_line(path: &Path, line: &str) {
    if let Ok(mut f) = OpenOptions::new().create(true).append(true).open(path) {
        let _ = writeln!(f, "{line}");
    }
}

/// A path as a file:// URL, for `node --import` (an absolute Windows path
/// would be read as a URL with the drive letter as its scheme).
pub fn file_url(path: &Path) -> String {
    let abs: PathBuf = std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    let s = abs.to_string_lossy().replace('\\', "/");
    // canonicalize on Windows gives \\?\C:\..., which becomes //?/C:/...
    let s = s.trim_start_matches("//?/");
    if s.starts_with('/') {
        format!("file://{s}")
    } else {
        format!("file:///{s}")
    }
}
