//go:build !windows && !linux && !darwin

package proc

func exeOf(int) (string, error) { return "", ErrNoExe }
