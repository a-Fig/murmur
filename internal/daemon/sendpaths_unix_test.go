//go:build unix

package daemon_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
)

// A file swapped for a named pipe between the look and the opening is refused at once: opening the
// pipe doesn't wait for a writer, which would hold the send for good (Sol's review of #773). Not
// parallel: the hook is the package's.
func TestSendRefusesAFileSwappedForAPipe(t *testing.T) {
	remote := newRemote(t)
	temp := t.TempDir()
	a := newDeviceWith(t, remote, func(d *device) { d.tempDir = temp })
	a.session("aaaaaa", "")
	path := putFile(t, filepath.Join(temp, "shot.png"))
	swapped := false
	t.Cleanup(daemon.SetSendRace(func(part string) {
		if part != "shot.png" || swapped {
			return
		}
		swapped = true
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Error(err)
		}
	}))
	done := make(chan string, 1)
	go func() { done <- sendOne(a, "aaaaaa", path) }()
	select {
	case err := <-done:
		if err == "" {
			t.Error("a file swapped for a pipe was sent")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the send waited on the pipe")
	}
	if !swapped {
		t.Error("the swap never ran")
	}
}

// A folder on the path swapped for a named pipe just before it is opened is refused at once too
// (Sol's second review of #773): os.Root's opening of a folder would wait on the pipe for a writer.
func TestSendRefusesAFolderSwappedForAPipe(t *testing.T) {
	remote := newRemote(t)
	temp := t.TempDir()
	a := newDeviceWith(t, remote, func(d *device) { d.tempDir = temp })
	a.session("aaaaaa", "")
	path := putFile(t, filepath.Join(temp, "shots", "shot.png"))
	swapped := false
	t.Cleanup(daemon.SetSendRace(func(part string) {
		if part != "shots" || swapped {
			return
		}
		swapped = true
		if err := os.Rename(filepath.Join(temp, "shots"), filepath.Join(temp, "shots-was")); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(filepath.Join(temp, "shots"), 0o600); err != nil {
			t.Error(err)
		}
	}))
	done := make(chan string, 1)
	go func() { done <- sendOne(a, "aaaaaa", path) }()
	select {
	case err := <-done:
		if err == "" {
			t.Error("a file below a folder swapped for a pipe was sent")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the send waited on the pipe")
	}
	if !swapped {
		t.Error("the swap never ran")
	}
}
