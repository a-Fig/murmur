package view

import (
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
)

// A notice from Murmur of how far a message got (#276) has no sender to describe or answer, in the
// turn, the inbox and the hook's notice alike.
func TestAReceiptNoticeHasNoSenderToAnswer(t *testing.T) {
	m := proto.MessageView{ID: "n1", From: "dev/receipts", FromName: brand.Product, To: "dev/aaaaaa",
		Subject: `quiet-otter has read your message "Take #12 over?"`, Body: "- quiet-otter: read, 1m ago",
		At: time.Now(), Receipt: true}

	if got, want := Turn(m), brand.Product+": "+m.Subject+"\n\n"+m.Body+"\n"; got != want {
		t.Errorf("the turn:\n%s\nwant:\n%s", got, want)
	}
	inbox := Inbox(proto.Response{Messages: []proto.MessageView{m}})
	for _, want := range []string{"1 message(s) from " + brand.Product + ", about your messages.", m.Subject, m.Body} {
		if !strings.Contains(inbox, want) {
			t.Errorf("the inbox lacks %q:\n%s", want, inbox)
		}
	}
	if strings.Contains(inbox, "Reply to") {
		t.Errorf("the inbox says to reply to a notice:\n%s", inbox)
	}
	if got, want := Notices([]proto.MessageView{m}, 0), "["+brand.Product+"] "+m.Subject+"\n"; !strings.HasPrefix(got, want) {
		t.Errorf("the hook's notice is %q, want it to start %q", got, want)
	}
}
