package desktop

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Detach starts the window's process on its own, with no console: mur is a console program, and
// the window outlives the terminal it was asked for in. A terminal in a job that forbids breaking
// away (an agent's) keeps it in the job, which ends it with the terminal. exited gets the
// process's end.
func Detach(exe string, args []string, out *os.File) (exited <-chan error, err error) {
	const flags = windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	cmd := command(exe, args, out, flags|windows.CREATE_BREAKAWAY_FROM_JOB)
	err = cmd.Start()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		cmd = command(exe, args, out, flags)
		err = cmd.Start()
	}
	if err != nil {
		return nil, err
	}
	return waited(cmd), nil
}

func command(exe string, args []string, out *os.File, flags uint32) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
	return cmd
}
