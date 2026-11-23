package proc

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

func exeOf(pid int) (string, error) {
	if uint64(pid) > math.MaxUint32 {
		return "", ErrGone
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // what OpenProcess says of an ID no process has
			return "", ErrGone
		}
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		// A handle can still be opened to an exited process while anything holds one to it;
		// QueryFullProcessImageName then fails with ERROR_GEN_FAILURE or ERROR_PARTIAL_COPY.
		var code uint32
		if windows.GetExitCodeProcess(h, &code) == nil && code != stillActive {
			return "", ErrGone
		}
		return "", err
	}
	if n == 0 {
		return "", ErrGone
	}
	return windows.UTF16ToString(buf[:n]), nil
}
