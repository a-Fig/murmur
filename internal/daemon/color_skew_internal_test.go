package daemon

import (
	"testing"
	"time"
)

// A live device whose clock runs over a week behind keeps its colour on a reader that has been
// awake and syncing: its every heartbeat carries a commit time a week back, but this reader's last
// good sync before the read was seconds ago (#1169).
func TestALiveDeviceWithAClockAWeekBehindKeepsItsColour(t *testing.T) {
	t.Parallel()
	d := colorRig(t, 0) // its presence changed just now
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Color, d.st.ColorAt = 0, time.Time{}
	p := d.peers["dead-0001"]
	p.sha, p.listed, p.writtenOf = "aaaa", "aaaa", "aaaa"
	p.written = time.Now().Add(-8 * 24 * time.Hour) // the writer's clock is 8 days behind
	p.colorFrom = colorDate(p.written, time.Now().Add(-5*time.Second))
	if got := d.colorsLocked()["dead-0001"]; !got.Picked {
		t.Errorf("a live device's colour is %+v: its clock being a week behind took it away", got)
	}
}

// colorDate takes the writer's clock, but no earlier than the reader's last good sync, and the
// writer's clock alone before a first one.
func TestColorDate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	week := 8 * 24 * time.Hour
	for _, c := range []struct {
		name              string
		written, lastGood time.Time
		want              time.Time
	}{
		{"an awake reader, a writer a week behind", now.Add(-week), now.Add(-5 * time.Second), now.Add(-5 * time.Second)},
		{"a reader that slept the week", now.Add(-week), now.Add(-week - time.Hour), now.Add(-week)},
		{"a writer ahead of the reader", now.Add(time.Hour), now, now.Add(time.Hour)},
		{"before a first good sync", now.Add(-week), time.Time{}, now.Add(-week)},
	} {
		if got := colorDate(c.written, c.lastGood); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
