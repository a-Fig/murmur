package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"
	"github.com/a-Fig/murmur/internal/t3db"
)

// A read of T3's database that fails, as one would after a T3 update changed its tables (#1016),
// says nothing about the thread: the read just before a handoff doesn't take the session as at
// work, the state read before stands, and the message is handed over. Here the handoff goes as far
// as starting T3's command line, which isn't there.
func TestAFailedThreadReadDoesNotHoldAHandoff(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	k := threadKey{"", "thread-aaaaaa"}
	before := t3db.State{LastRun: time.Now().Add(-time.Minute), Model: "claude-opus-5-5"}
	d.threads[k] = before
	reads := 0
	d.opt.ThreadState = func(context.Context, string, string, string) (t3db.State, error) {
		reads++
		// What StateOf gives for a column that isn't there: an error, and no State.
		return t3db.State{}, errors.New("reading T3's database: SQL logic error: no such column: r.status")
	}
	m := &inMsg{Event: bus.Event{ID: "m", Type: bus.Message, From: "dev2/bbbbbb", To: "dev/aaaaaa"}, Arrived: time.Now(), Pushing: true}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": {m}}
	j := &pushJob{id: "aaaaaa", addr: "dev/aaaaaa", msgs: []*inMsg{m}, via: viaT3,
		route: proto.Route{T3CLI: "/no/such/t3", T3Thread: "thread-aaaaaa"}}

	if d.t3RunGoing(context.Background(), j, pageSettings{}) {
		t.Error("t3RunGoing took a failed read for a run going")
	}
	err := d.sendTurn(context.Background(), j)
	if errors.Is(err, errNotNow) || !errors.Is(err, t3.ErrNotStarted) {
		t.Fatalf("sendTurn = %v; want the handoff tried, and failing only to start T3's command line", err)
	}
	if reads != 2 {
		t.Errorf("T3's database was read %d times, want once by each handoff check", reads)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.threads[k]; got != before || !d.threadRead[k].IsZero() {
		t.Errorf("after the failed reads the thread is %+v, read at %v; want %+v, unread", got, d.threadRead[k], before)
	}
	if d.hookBusyLocked("aaaaaa") {
		t.Error("the session is at work, by reads that failed")
	}
}
