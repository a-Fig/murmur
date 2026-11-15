package proc

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
)

func exeOf(pid int) (string, error) {
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrGone
	}
	return exe, err
}
