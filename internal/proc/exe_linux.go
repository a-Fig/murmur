package proc

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func exeOf(pid int) (string, error) {
	if pid > math.MaxInt32 {
		return "", ErrGone
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			return "", ErrGone
		}
		return "", err
	}
	if exe == "" {
		return "", ErrGone
	}
	return exe, nil
}
