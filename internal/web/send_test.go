package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// heldSource is a fake whose Send waits to be let go, and then says whether its context had ended:
// a push the daemon runs is killed with it.
type heldSource struct {
	*fakeSource
	entered chan struct{}
	release chan struct{}
	ended   chan error
}

func (f *heldSource) Send(ctx context.Context, to, text string, files []Upload) (Sent, error) {
	close(f.entered)
	<-f.release
	// The server notices the closed connection a moment after the client goes: a context that
	// ends with the request has ended within the second.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	f.ended <- ctx.Err()
	return f.fakeSource.Send(ctx, to, text, files)
}

// A page closed or reloaded while its message is on its way doesn't take the message with it
// (#613): the send goes on without the request.
func TestASendOutlivesThePageThatAskedForIt(t *testing.T) {
	src := &heldSource{fakeSource: newFake(), entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan error, 1)}
	h := start(t, src, noPRs)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", h.srv.URL+"/api/send", strings.NewReader(`{"to":"*","text":"before I go"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:" + h.port
	req.Header.Set("Authorization", "Bearer "+testToken)
	done := make(chan error, 1)
	go func() {
		resp, err := h.srv.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-src.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the send never reached the source")
	}
	cancel() // the page goes
	if err := <-done; err == nil {
		t.Fatal("the request finished though the page went")
	}
	close(src.release)
	if err := <-src.ended; err != nil {
		t.Errorf("the send's context ended with the page: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		src.mu.Lock()
		got := append([]string(nil), src.sends...)
		src.mu.Unlock()
		if len(got) == 1 && got[0] == "*: before I go" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sent %q", got)
		}
	}
}
