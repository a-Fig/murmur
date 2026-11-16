package daemon

import (
	"testing"
	"time"
)

// A gone device's files ref is due only once both dates say so (#708): a clock set forward makes
// the first read look old, and a teammate's event can carry any sending date.
func TestOthersFileDue(t *testing.T) {
	keep := 14 * 24 * time.Hour
	old, recent := time.Now().Add(-keep-time.Hour), time.Now().Add(-time.Minute)
	for what, c := range map[string]struct {
		f    othersFile
		want bool
	}{
		"both old":                     {othersFile{Seen: old, Sent: old}, true},
		"this clock set forward since": {othersFile{Seen: old, Sent: recent}, false},
		"sent long ago by its event":   {othersFile{Seen: recent, Sent: old}, false},
		"with no sending date":         {othersFile{Seen: old}, false},
	} {
		if got := c.f.due(keep); got != c.want {
			t.Errorf("%s: %v, want %v", what, got, c.want)
		}
	}
}
