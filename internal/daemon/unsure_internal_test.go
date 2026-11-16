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
	if err.Error() != context.DeadlineExceeded.Error() {
		t.Errorf("an unsure error reads %q", err)
	}
}
