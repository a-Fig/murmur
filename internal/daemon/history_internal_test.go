package daemon

import (
	"context"
	"io"
	"log"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/web"
)

func historyDaemon(pageMessages int) *Daemon {
	return &Daemon{opt: Options{PageMessages: pageMessages}, log: log.New(io.Discard, "", 0), device: "here-0001",
		started: time.Now(), msgIDs: map[string]bool{}, actIDs: map[string]bool{}}
}

var historyBase = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

// sentAt is a message with id as its ID and subject, sent min minutes into the test's morning.
func sentAt(id string, min int) bus.Event {
	return bus.Event{V: bus.Version, ID: id, Type: bus.Message, From: "there-0002/aaaaaa", To: "*", Subject: id,
		At: historyBase.Add(time.Duration(min) * time.Minute)}
}

func (d *Daemon) pageSubjects() (subjects []string, seqs []uint64) {
	for _, m := range d.msgs {
		subjects, seqs = append(subjects, m.e.Subject), append(seqs, m.seq)
	}
	return subjects, seqs
}

// What is read back goes among the page's messages by when it was sent, each once, and the page's
// limit keeps the newest. A page that asked before is given them all again, as their numbers are
// new; it knows the ones it has by their IDs.
func TestHistoryMergesByTimeAndRenumbers(t *testing.T) {
	d := historyDaemon(4)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range []bus.Event{sentAt("m3", 3), sentAt("m5", 5)} {
		d.keepMessageLocked(e)
	}
	before := d.msgSeq

	d.mergeMessagesLocked([]bus.Event{sentAt("m1", 1), sentAt("m2", 2), sentAt("m3", 3), sentAt("m4", 4), sentAt("m4", 4)})
	got, seqs := d.pageSubjects()
	if !slices.Equal(got, []string{"m2", "m3", "m4", "m5"}) {
		t.Fatalf("the page holds %q, want the newest four in the order sent", got)
	}
	for i, seq := range seqs {
		if seq <= before || i > 0 && seq <= seqs[i-1] {
			t.Fatalf("numbers %v: each must pass %d and the one before, or a cursor from before misses it", seqs, before)
		}
	}
	if len(d.msgIDs) != 4 || d.msgIDs["m1"] || !d.msgIDs["m4"] {
		t.Errorf("the IDs known are %v", d.msgIDs)
	}
}

// With nothing new to keep, the page's messages are left as they were, numbers and all: a page isn't
// given again what it has each time the logs are read back. That holds when what was read is all
// older than a full page's messages, as when a message was dropped while the read was under way.
func TestHistoryWithNothingToAddChangesNothing(t *testing.T) {
	d := historyDaemon(3)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range []bus.Event{sentAt("m3", 3), sentAt("m4", 4), sentAt("m5", 5)} {
		d.keepMessageLocked(e)
	}
	_, was := d.pageSubjects()
	for _, read := range [][]bus.Event{
		nil,
		{sentAt("m3", 3), sentAt("m5", 5)},
		{sentAt("m1", 1), sentAt("m2", 2), sentAt("m4", 4)},
	} {
		d.mergeMessagesLocked(read)
		got, seqs := d.pageSubjects()
		if !slices.Equal(got, []string{"m3", "m4", "m5"}) || !slices.Equal(seqs, was) || d.msgSeq != was[2] {
			t.Fatalf("after reading back %d: %q numbered %v up to %d, want them as they were, %v", len(read), got, seqs, d.msgSeq, was)
		}
		if len(d.msgIDs) != 3 || d.msgIDs["m1"] {
			t.Fatalf("the IDs known are %v", d.msgIDs)
		}
	}
}

// A page that holds a cursor when earlier messages are read back is given every message again, in
// order, the ones it has among them: it passes over those by their IDs. Its next call brings
// nothing more. A read that adds nothing leaves the cursor it holds good.
func TestHistoryIsGivenToAPageThatHoldsACursor(t *testing.T) {
	d := historyDaemon(4)
	src := pageSource{d}
	ctx := context.Background()
	call := func(cursor string) ([]string, string) {
		t.Helper()
		msgs, next, err := src.Messages(ctx, cursor)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range msgs {
			got = append(got, m.Subject)
		}
		return got, next
	}
	merge := func(events ...bus.Event) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.mergeMessagesLocked(events)
	}

	d.mu.Lock()
	d.keepMessageLocked(sentAt("m3", 3))
	d.keepMessageLocked(sentAt("m5", 5))
	d.mu.Unlock()
	got, held := call("")
	if !slices.Equal(got, []string{"m3", "m5"}) {
		t.Fatalf("the page's first call: %q", got)
	}

	merge(sentAt("m3", 3), sentAt("m5", 5))
	if got, next := call(held); len(got) != 0 || next != held {
		t.Fatalf("after a read that added nothing, the cursor brought %q and became %q, from %q", got, next, held)
	}

	merge(sentAt("m1", 1), sentAt("m4", 4))
	got, next := call(held)
	if !slices.Equal(got, []string{"m1", "m3", "m4", "m5"}) {
		t.Fatalf("after earlier messages were read back, the cursor the page held brought %q, want all four in order", got)
	}
	if got, again := call(next); len(got) != 0 || again != next {
		t.Errorf("the call after that brought %q", got)
	}
	if got, _ := call(""); !slices.Equal(got, []string{"m1", "m3", "m4", "m5"}) {
		t.Errorf("a page that starts now is given %q", got)
	}
}

// A read of the logs that doesn't come, as with a git that hangs, costs the page one wait: the call
// that waited it out says so, and no call after it waits.
func TestAReadThatDoesNotComeIsWaitedForOnce(t *testing.T) {
	d := historyDaemon(4)
	d.historyRead, d.historyWant, d.bgCtx = make(chan struct{}), make(chan struct{}), context.Background()
	began := time.Now()
	d.awaitHistory(context.Background())
	if took := time.Since(began); took < historyWait {
		t.Errorf("the first call waited %s, want the whole %s", took, historyWait)
	}
	select {
	case <-d.historyWant:
	default:
		t.Error("the call didn't ask for the logs to be read back")
	}
	began = time.Now()
	d.awaitHistory(context.Background())
	if took := time.Since(began); took > historyWait/2 {
		t.Errorf("a second call waited %s for a read the first had waited out", took)
	}
}

// A person's own messages are counted apart from the rest when history is merged, as when they
// are read.
func TestHistoryCountsAPersonsMessagesApart(t *testing.T) {
	d := historyDaemon(2)
	d.mu.Lock()
	defer d.mu.Unlock()
	own := sentAt("mine", 1)
	own.To = d.personAddr()
	d.mergeMessagesLocked([]bus.Event{own, sentAt("m2", 2), sentAt("m3", 3), sentAt("m4", 4)})
	if got, _ := d.pageSubjects(); !slices.Equal(got, []string{"mine", "m3", "m4"}) || d.ownMsgs != 1 {
		t.Errorf("the page holds %q with %d of the person's, want theirs and the newest two", got, d.ownMsgs)
	}
}

// The Recent view's activity merges the same way.
func TestHistoryMergesActivity(t *testing.T) {
	d := historyDaemon(3)
	d.mu.Lock()
	defer d.mu.Unlock()
	act := func(id string, min int) web.Activity {
		return web.Activity{ID: id, Kind: web.ActStarted, Addr: "there-0002/aaaaaa", At: historyBase.Add(time.Duration(min) * time.Minute)}
	}
	d.recordActLocked(act("a2", 2))
	d.recordActLocked(web.Activity{Kind: web.ActStatus, Addr: "there-0002/aaaaaa", Doing: "now", At: historyBase.Add(4 * time.Minute)})
	before := d.actSeq

	d.mergeActsLocked([]web.Activity{act("a2", 2)})
	if d.actSeq != before || len(d.acts) != 2 {
		t.Fatalf("reading back what it has changed the activity: %d kept, numbered to %d", len(d.acts), d.actSeq)
	}
	d.mergeActsLocked([]web.Activity{act("a1", 1), act("a3", 3), act("a2", 2)})
	var kinds []string
	for i, p := range d.acts {
		kinds = append(kinds, p.a.Kind+" "+p.a.At.Sub(historyBase).String())
		if p.seq <= before || i > 0 && p.seq <= d.acts[i-1].seq {
			t.Fatalf("activity %d is numbered %d, after %d", i, p.seq, before)
		}
	}
	if !slices.Equal(kinds, []string{web.ActStarted + " 1m0s", web.ActStarted + " 2m0s", web.ActStarted + " 3m0s", web.ActStatus + " 4m0s"}) {
		t.Errorf("the activity is %q, want the three starts and then the status", kinds)
	}
}

// Two messages with one ID are two messages (#603): another device's, a copy of the ID with an
// earlier time, and the sender's own later one. Each is on the page, under an ID of its own. The
// same event read again is still kept once, and what the limit drops is forgotten whole.
func TestAReusedMessageIDHidesNothing(t *testing.T) {
	d := historyDaemon(10)
	d.mu.Lock()
	defer d.mu.Unlock()
	first := sentAt("m1", 5)
	first.Subject = "the first"
	forged := sentAt("m1", 1)
	forged.From, forged.Subject = "other-0003/bbbbbb", "a copy of the ID, dated before"
	again := sentAt("m1", 9)
	again.Subject = "the sender's own, later"
	for _, e := range []bus.Event{first, forged, again, first, forged} {
		d.recordMessageLocked(e)
	}
	ids := map[string]string{}
	for _, m := range d.msgs {
		ids[m.id] = m.e.Subject
	}
	if len(d.msgs) != 3 || len(ids) != 3 || ids["m1"] != "the first" || len(d.msgIDs) != 3 || len(d.msgKeys) != 3 {
		t.Fatalf("the page holds %v, want the three messages, each under an ID of its own, the first under m1", ids)
	}

	// A restart reads them back in the order of the senders' clocks: all three still, and the same
	// event read twice still once.
	fresh := historyDaemon(10)
	fresh.mu.Lock()
	defer fresh.mu.Unlock()
	fresh.mergeMessagesLocked([]bus.Event{first, forged, again, again})
	got, _ := fresh.pageSubjects()
	if !slices.Equal(got, []string{"a copy of the ID, dated before", "the first", "the sender's own, later"}) {
		t.Errorf("read back, the page holds %q", got)
	}
	if !fresh.msgKeys[msgKey(first)] || len(fresh.msgIDs) != 3 {
		t.Errorf("read back, the IDs known are %v", fresh.msgIDs)
	}

	// Past the limit the oldest goes, and its ID stays its own: a page that loaded earlier still
	// holds it, and would pass over another message given that ID.
	small := historyDaemon(2)
	small.mu.Lock()
	defer small.mu.Unlock()
	for _, e := range []bus.Event{first, forged, again} {
		small.recordMessageLocked(e)
	}
	if len(small.msgs) != 2 || len(small.msgIDs) != 2 || len(small.msgKeys) != 2 || small.msgKeys[msgKey(first)] || small.msgIDs["m1"] {
		t.Errorf("past the limit: %d messages, IDs %v, %d keys", len(small.msgs), small.msgIDs, len(small.msgKeys))
	}
	// A merge sorts the list again, and each message keeps its ID.
	was := map[string]string{}
	for _, m := range small.msgs {
		was[m.e.Subject] = m.id
	}
	late := sentAt("m9", 20)
	small.mergeMessagesLocked([]bus.Event{late})
	for _, m := range small.msgs {
		if id, held := was[m.e.Subject]; held && id != m.id {
			t.Errorf("%q was %s to the page and is %s after a merge", m.e.Subject, id, m.id)
		}
	}
	// Read again, the first is itself again, under its own ID.
	big := historyDaemon(10)
	big.mu.Lock()
	defer big.mu.Unlock()
	big.idOwner, big.idGone = small.idOwner, small.idGone
	big.recordMessageLocked(again)
	big.recordMessageLocked(first)
	for _, m := range big.msgs {
		if (m.id == "m1") != (m.e.Subject == "the first") {
			t.Errorf("%q is %s to the page", m.e.Subject, m.id)
		}
	}
	// Until enough has been dropped since that no page can still hold it.
	for i := 0; i <= idKeep; i++ {
		small.recordMessageLocked(sentAt("x"+strconv.Itoa(i), 30+i))
	}
	if _, owned := small.idOwner["m1"]; owned || len(small.idGone) > idKeep || len(small.idOwner) > idKeep+2 {
		t.Errorf("after %d more messages m1 is still spoken for, or the IDs kept grew: %d gone, %d owned", idKeep, len(small.idGone), len(small.idOwner))
	}

	// The person's unread goes by the ID the page knows a message by.
	d.st.Person = "ada-lovelace"
	d.st.Inbox = map[string][]*inMsg{bus.PersonSession: {{Event: forged}, {Event: first}}}
	if got := d.accountLocked().Unread; len(got) != 2 || got[0] == got[1] || got[1] != "m1" {
		t.Errorf("unread, with two messages of one ID: %v", got)
	}
}
