package proc

import (
	"math"
	"strconv"
)

func ancestorsOf(pid, max int) []int {
	return walkAncestors(pid, max, parentOf)
}

// parentOf is pid's parent, the fourth field of /proc/<pid>/stat.
func parentOf(pid int) (int, bool) {
	if pid <= 0 || pid > math.MaxInt32 {
		return 0, false
	}
	return readLinuxStatPPID("/proc/" + strconv.Itoa(pid) + "/stat")
}
