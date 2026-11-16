package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
)

// A push the remote refused, or that was never sent, didn't land; any other failure may have
// (#745). What was made for the event is dropped only for the first kind.
func TestOnlyAPushKnownNotToHaveLandedIsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err     error
		refused bool
	}{
		{bus.ErrRejected, true},
		{fmt.Errorf("push to refs/murmur/mesh/x %w: hook declined", bus.ErrRefused), true},
		{ErrNoConsent, true},
		{ErrInvisible, true},
		// Out of time before git was started: nothing was sent.
		{notPushedError{context.DeadlineExceeded}, true},
		{fmt.Errorf("writing: %w", notPushedError{context.Canceled}), true},
		// Out of time with the push under way, which may have landed.
		{context.DeadlineExceeded, false},
		{fmt.Errorf("git push: %w", context.Canceled), false},
		{errors.New("fatal: unable to access the remote: connection reset"), false},
	} {
		if got := pushRefused(c.err); got != c.refused {
			t.Errorf("pushRefused(%v) = %v", c.err, got)
		}
	}
	err := error(unsureError{context.DeadlineExceeded})
	if !errors.As(fmt.Errorf("not sent: %w", err), new(unsureError)) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an unsure error doesn't show as one, or hides what it wraps")
	}
	if err.Error() != context.DeadlineExceeded.Error() || (notPushedError{context.Canceled}).Error() != context.Canceled.Error() {
		t.Errorf("a wrapped error doesn't read as what it wraps: %q", err)
	}
}
