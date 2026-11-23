//go:build !windows && !linux && !darwin

package proc

func ancestorsOf(pid, max int) []int {
	return walkAncestors(pid, max, parentOf)
}

// parentOf finds nothing on systems without a way to look written here.
func parentOf(int) (int, bool) { return 0, false }
