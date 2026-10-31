package main

// The server runs in a Job Object that kills every process in it when the
// job's last handle closes. The shell holds that handle, so the server and
// everything it started end when the shell stops it, and also when the
// shell itself dies without stopping it. The server is created suspended,
// put in the job, then resumed, so not even its first child can start
// outside the job.

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type serverProcess struct {
	cmd  *exec.Cmd
	job  windows.Handle
	done chan struct{}
}

func startServerProcess(cmd *exec.Cmd) (*serverProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	// No console window for node.exe (the shell is a GUI program), and
	// suspended until it is in the job.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED,
	}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	p := &serverProcess{cmd: cmd, job: job, done: make(chan struct{})}
	pid := uint32(cmd.Process.Pid)
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		windows.CloseHandle(job)
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	if err := resumeProcess(pid); err != nil {
		_ = cmd.Process.Kill()
		windows.CloseHandle(job)
		return nil, err
	}
	return p, nil
}

// resumeProcess resumes the threads of a process created suspended (Go's
// exec does not hand back the main thread's handle).
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			continue
		}
		if _, err := windows.ResumeThread(th); err == nil {
			resumed++
		}
		windows.CloseHandle(th)
	}
	if resumed == 0 {
		return fmt.Errorf("could not resume the server (pid %d)", pid)
	}
	return nil
}

func (p *serverProcess) exited() { close(p.done) }

// stop: Windows has no TERM for a console-less child, so the job is
// terminated, which ends the server and everything it started at once. A
// graceful stop would need the server to offer one (an endpoint, or
// exiting when its stdin closes).
func (p *serverProcess) stop(timeout time.Duration) {
	p.killTree()
	select {
	case <-p.done:
	case <-time.After(timeout):
	}
}

func (p *serverProcess) killTree() { _ = windows.TerminateJobObject(p.job, 1) }

func (p *serverProcess) close() {
	if p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
}
