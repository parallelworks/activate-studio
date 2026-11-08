package extract

import "os"

// The tests that use these skip on Windows (stubborn); they only compile.
func alive(pid int) bool { return false }

func killHard(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}
