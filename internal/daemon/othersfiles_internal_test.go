package daemon

import (
	"testing"
	"time"
)

// A gone device's files ref ages by the time this daemon has run, on the monotonic clock (#708): a
// wall clock set forward by weeks ages nothing, nor does the time the daemon was down, and a gap
// longer than two prunes counts as two.
func TestOthersFilesAge(t *testing.T) {
	d := &Daemon{opt: Options{FilePrune: time.Hour}, st: state{OthersFiles: map[string]othersFile{"r": {Device: "dev"}}}}
	start := time.Now() // with a monotonic reading, as the daemon's are
	d.ageOthersFilesLocked(start)
	if got := d.st.OthersFiles["r"].Age; got != 0 {
		t.Fatalf("the first call, as the daemon starts, aged it %v", got)
	}
	d.ageOthersFilesLocked(start.Add(time.Hour))
	d.ageOthersFilesLocked(start.Add(time.Hour).Add(15 * 24 * time.Hour))
	if got := d.st.OthersFiles["r"].Age; got != 3*time.Hour {
		t.Errorf("after an hour, then a gap of 15 days: %v, want 3h", got)
	}
	// A wall clock set forward moves no monotonic reading: the same reading ages nothing.
	d.ageOthersFilesLocked(start.Add(time.Hour).Add(15 * 24 * time.Hour))
	if got := d.st.OthersFiles["r"].Age; got != 3*time.Hour {
		t.Errorf("aged by no time passing: %v", got)
	}
}
