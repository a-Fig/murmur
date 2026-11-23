package daemon

import (
	"errors"
	"fmt"
	"testing"

	"github.com/a-Fig/murmur/internal/clientcli"
	"github.com/a-Fig/murmur/internal/msgsock"
	"github.com/a-Fig/murmur/internal/t3"
)

// A handoff is made a second time only if the first reached nothing and the same call might work a
// moment later. One t3.Send refused for what it was asked to send to reached nothing either, so
// its message isn't marked as one that may have landed, but it would be refused again (#1164).
func TestOnlyAnAttemptThatMightWorkIsMadeAgain(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err            error
		reached, again bool
	}{
		{"t3 didn't start", fmt.Errorf("t3 thread send: %w: no such file", t3.ErrNotStarted), false, true},
		{"the socket wasn't there", fmt.Errorf("socket: %w", msgsock.ErrNotConnected), false, true},
		{"a client's command didn't start", fmt.Errorf("codex: %w", clientcli.ErrNotStarted), false, true},
		{"a thread ID t3 would read as an option", fmt.Errorf("t3 thread send: %w: invalid thread ID", t3.ErrRefused), false, false},
		{"t3 ran and failed", errors.New("t3 thread send: exit status 1"), true, false},
	} {
		if got := !notReached(tc.err); got != tc.reached {
			t.Errorf("%s: may have reached the client is %v, want %v", tc.name, got, tc.reached)
		}
		if got := worthAnotherTry(tc.err); got != tc.again {
			t.Errorf("%s: tried again is %v, want %v", tc.name, got, tc.again)
		}
	}
}
