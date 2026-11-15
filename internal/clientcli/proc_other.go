//go:build !windows

package clientcli

import "os/exec"

func hideWindow(*exec.Cmd) {}

// fits is true: the limits here (128 KiB an argument on Linux, 1 MiB in all on macOS) are over
// what a message's 32 KiB comes to, and a start that fails on one is ErrNotStarted.
func fits(string, []string) bool { return true }
