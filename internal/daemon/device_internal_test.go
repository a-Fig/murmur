package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A new clone's device ID says nothing of the machine, as anyone who can read the repo sees it
// (#330); a clone that has an ID keeps it, hostname and all.
func TestADeviceIDSaysNothingOfTheMachine(t *testing.T) {
	dir := t.TempDir()
	id, err := loadDevice(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^device-[0-9a-f]{8}$`).MatchString(id) {
		t.Errorf("a new device's ID is %q, want device- and eight hex digits", id)
	}
	if host, _ := os.Hostname(); host != "" && strings.Contains(id, strings.ToLower(strings.Split(host, ".")[0])) {
		t.Errorf("the ID %q carries the hostname %q", id, host)
	}
	if again, _ := loadDevice(dir); again != id {
		t.Errorf("the ID changed from %q to %q on a second start", id, again)
	}

	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "device"), []byte("tyler-laptop-3f2a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadDevice(old); got != "tyler-laptop-3f2a" {
		t.Errorf("a clone's existing ID became %q", got)
	}
}
