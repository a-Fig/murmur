package proc

import (
	"math"

	"golang.org/x/sys/unix"
)

func ancestorsOf(pid, max int) []int {
	return walkAncestors(pid, max, parentOf)
}

// parentOf is pid's parent, from the kernel's process table.
func parentOf(pid int) (int, bool) {
	if pid <= 0 || pid > math.MaxInt32 {
		return 0, false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid || kp.Eproc.Ppid <= 0 {
		return 0, false
	}
	return int(kp.Eproc.Ppid), true
}
