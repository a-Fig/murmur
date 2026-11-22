package mcpserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// A daemon from before tell_me (#276) passes over it, and its reply has no Telling: the sender is
// told it won't be told. A daemon that is telling, a refusal and a send that didn't ask add nothing.
func TestUntoldSaysAnOlderDaemonWontTell(t *testing.T) {
	old := untold(proto.Response{OK: true, Note: "Sent."}, nil, "read")
	if !strings.Contains(old.Note, "You won't be told how far it gets") || !strings.HasPrefix(old.Note, "Sent. ") {
		t.Errorf("an older daemon's reply: %q", old.Note)
	}
	for _, c := range []struct {
		resp proto.Response
		err  error
		tell string
	}{
		{proto.Response{OK: true, Telling: true, TellKnown: true}, nil, "read"},
		{proto.Response{OK: true, TellKnown: true, Note: "You won't be told how far it gets: it reached no session whose Murmur sends receipts."}, nil, "read"},
		{proto.Response{OK: true}, nil, ""},
		{proto.Response{Error: "not sent"}, nil, "read"},
		{proto.Response{}, errors.New("no daemon"), "read"},
	} {
		if got := untold(c.resp, c.err, c.tell); got.Note != c.resp.Note {
			t.Errorf("untold(%+v, %v, %q) noted %q", c.resp, c.err, c.tell, got.Note)
		}
	}
}
