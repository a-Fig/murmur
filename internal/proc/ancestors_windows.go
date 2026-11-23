package proc

import (
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ancestorsOf walks pid's ancestors from a single system process snapshot, stopping on Windows PID
// reuse when both creation times can be read and the candidate parent is strictly newer than its
// child.
func ancestorsOf(pid, max int) []int {
	if max <= 0 || pid <= 0 {
		return nil
	}
	if max == 1 || uint64(pid) > math.MaxUint32 {
		return []int{pid}
	}
	parents := snapshotParents()
	if len(parents) == 0 {
		return []int{pid}
	}
	created := map[int]uint64{}
	createdKnown := map[int]bool{}
	creationOf := func(p int) (uint64, bool) {
		if ok, cached := createdKnown[p]; cached {
			return created[p], ok
		}
		t, ok := processCreationTime(p)
		createdKnown[p] = ok
		if ok {
			created[p] = t
		}
		return t, ok
	}
	return walkAncestors(pid, max, func(cur int) (int, bool) {
		if cur <= 0 || uint64(cur) > math.MaxUint32 {
			return 0, false
		}
		ppid, ok := parents[uint32(cur)]
		if !ok || ppid == 0 {
			return 0, false
		}
		parent := int(ppid)
		if parent <= 0 {
			return 0, false
		}
		if parentNewerThanChild(cur, parent, creationOf) {
			return 0, false
		}
		return parent, true
	})
}

func snapshotParents() map[uint32]uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	parents := make(map[uint32]uint32, 128)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID != 0 {
			parents[e.ProcessID] = e.ParentProcessID
		}
	}
	return parents
}

func processCreationTime(pid int) (uint64, bool) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return 0, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	t := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if t == 0 {
		return 0, false
	}
	return t, true
}
