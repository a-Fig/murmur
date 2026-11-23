package daemon

import "testing"

// A daemon with no dir must never resolve state.json to the working directory (#872).
func TestStatePathRefusesEmptyDir(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("statePath on a Daemon with empty dir did not panic")
		}
	}()
	(&Daemon{}).statePath()
}
