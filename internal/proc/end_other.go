//go:build !windows && !linux && !darwin

package proc

func end(int, func(string) bool) error { return ErrCantEnd }
