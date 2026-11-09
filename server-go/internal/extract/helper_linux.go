package extract

import "syscall"

// parentDeath has the kernel kill the helper if the pass dies first, as it
// does when the server stops it with SIGKILL.
func parentDeath(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
