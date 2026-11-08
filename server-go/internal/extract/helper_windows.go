package extract

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tree is a helper's job object. The helper starts suspended, joins the
// job and only then runs, so every process it starts is in the job too.
// The job kills them all when it is terminated, on timeout or
// cancellation, and when its last handle closes: after the helper exits,
// which takes a leftover soffice.bin with it, and when the pass itself
// dies.
type tree struct {
	mu  sync.Mutex
	job windows.Handle
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

func (t *tree) prepare(cmd *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return // the command alone is killed, as exec does by default
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	t.job = job
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	cmd.Cancel = func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.job != 0 {
			windows.TerminateJobObject(t.job, 1)
		}
		if cmd.Process != nil {
			cmd.Process.Kill() // in case it never joined the job
		}
		return nil
	}
}

// attach puts the started, suspended helper in the job and resumes it.
func (t *tree) attach(cmd *exec.Cmd) error {
	if t.job == 0 {
		return nil
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("helper: open process: %w", err)
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(t.job, p); err != nil {
		return fmt.Errorf("helper: job object: %w", err)
	}
	if st, _, _ := ntResumeProcess.Call(uintptr(p)); st != 0 {
		return fmt.Errorf("helper: resume: status %#x", st)
	}
	return nil
}

// release closes the job, killing whatever the helper left running.
func (t *tree) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
}
