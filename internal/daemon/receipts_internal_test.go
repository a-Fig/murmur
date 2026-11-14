package daemon

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

func receiptDaemon(t *testing.T) *Daemon {
	return &Daemon{device: "here-0001", dir: t.TempDir(), log: log.New(io.Discard, "", 0), markKick: make(chan struct{}, 1),
		peers: map[string]*peer{}, sessions: map[string]*session{}}
}

// A recipient's marks gather: a message that goes from queued to read before the receipt is written
// costs one mark, at its furthest. One that didn't ask, or that went to the CLI's mailbox, costs none.
func TestMarksGatherAtTheirFurthest(t *testing.T) {
	d := receiptDaemon(t)
	asked := bus.Event{ID: "0123456789abcdef", Type: bus.Message, From: "there-0002/human", To: "here-0001/bbbbbb", Receipts: true}
	d.markLocked("bbbbbb", asked, bus.MarkQueued)
	d.markLocked("bbbbbb", asked, bus.MarkRead)
	d.markLocked("bbbbbb", asked, bus.MarkDelivered) // late, and behind: changes nothing
	d.markLocked(proto.CLISession, asked, bus.MarkQueued)
	d.markLocked("bbbbbb", bus.Event{ID: "fedcba9876543210", Type: bus.Message, From: "there-0002/aaaaaa"}, bus.MarkQueued)
	if len(d.st.Marks) != 1 || d.st.Marks[0].Msg != asked.ID || d.st.Marks[0].To != "bbbbbb" || d.st.Marks[0].State != bus.MarkRead {
		t.Errorf("the marks waiting: %+v, want one, read", d.st.Marks)
	}
	// A person's inbox is what they see as unread: delivered, not queued.
	d.markLocked(bus.PersonSession, bus.Event{ID: "00000000000000aa", Type: bus.Message, From: "there-0002/human", Receipts: true}, bus.MarkQueued)
	if m := d.st.Marks[len(d.st.Marks)-1]; m.To != bus.PersonSession || m.State != bus.MarkDelivered {
		t.Errorf("a person's mark: %+v", m)
	}
}

// The sender keeps the furthest mark per recipient, however the marks arrive, and shows a recipient
// whose device writes none as sending no receipt rather than as unread.
func TestTheSenderKeepsTheFurthestMarkAndSaysWhoSendsNone(t *testing.T) {
	d := receiptDaemon(t)
	d.opt.StaleAfter = time.Hour
	d.lastPollOK = time.Now()
	d.peers["new-0002"] = &peer{pres: bus.Presence{Receipts: true, Sessions: []bus.SessionInfo{{ID: "bbbbbb", Name: "quiet-otter"}}}, lastChange: time.Now()}
	d.peers["old-0003"] = &peer{pres: bus.Presence{Sessions: []bus.SessionInfo{{ID: "cccccc", Name: "brave-heron"}}}, lastChange: time.Now()}
	e := bus.Event{ID: "0123456789abcdef", Type: bus.Message, From: "here-0001/human", To: "*", At: time.Now(), Receipts: true}
	d.recordSentLocked(e)
	rec := d.st.Sent[e.ID]
	if rec == nil || len(rec.To) != 2 || len(rec.Old) != 1 || rec.Old[0] != "old-0003/cccccc" {
		t.Fatalf("the message as recorded: %+v", rec)
	}
	at := time.Now()
	d.applyReceiptLocked(bus.Event{Type: bus.Receipt, From: "new-0002/receipts", Marks: []bus.Mark{
		{Msg: e.ID, To: "bbbbbb", State: bus.MarkRead, At: at},
		{Msg: "ffffffffffffffff", To: "bbbbbb", State: bus.MarkRead, At: at}, // not one of ours
	}})
	d.applyMarkLocked(e.ID, "new-0002/bbbbbb", bus.MarkQueued, at.Add(time.Second)) // read late
	got := map[string]web.Receipt{}
	for _, r := range d.receiptsLocked()[e.ID] {
		got[r.Addr] = r
	}
	if r := got["new-0002/bbbbbb"]; r.State != bus.MarkRead || r.Name != "quiet-otter" || !r.At.Equal(at) {
		t.Errorf("the recipient that read it: %+v", r)
	}
	if r := got["old-0003/cccccc"]; r.State != web.ReceiptNone {
		t.Errorf("the recipient on an older Murmur: %+v, want %q", r, web.ReceiptNone)
	}
	if len(d.receiptsLocked()) != 1 {
		t.Errorf("a mark for a message that isn't ours was kept: %+v", d.st.Sent)
	}
}

// Only the newest maxSent messages keep their receipts.
func TestOnlyTheNewestSentMessagesKeepReceipts(t *testing.T) {
	d := receiptDaemon(t)
	start := time.Now()
	var first string
	for i := range maxSent + 5 {
		e := bus.Event{ID: bus.NewID(), Type: bus.Message, From: "here-0001/human", To: "here-0001/bbbbbb", At: start.Add(time.Duration(i) * time.Second)}
		if i == 0 {
			first = e.ID
		}
		d.recordSentLocked(e)
	}
	if len(d.st.Sent) != maxSent || d.st.Sent[first] != nil {
		t.Errorf("kept %d, the oldest among them: %v", len(d.st.Sent), d.st.Sent[first] != nil)
	}
}
