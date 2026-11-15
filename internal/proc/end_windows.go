package proc

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess gives for a process that hasn't exited.
const stillActive = 259

func end(pid int, is func(exe string) bool) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // what OpenProcess says of an ID no process has
			return ErrGone
		}
		return err
	}
	defer windows.CloseHandle(h)
	// A handle can be opened to a process that has exited, while anything still holds one to it.
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return err
	}
	if code != stillActive {
		return ErrGone
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return err
	}
	if !is(windows.UTF16ToString(buf[:n])) {
		return ErrOther
	}
	return windows.TerminateProcess(h, 1)
}
