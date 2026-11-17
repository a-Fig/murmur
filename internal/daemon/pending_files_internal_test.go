package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A pending send with files whose Send files switch goes off is taken out of the log and given up:
// neither its retry nor a later write carries it, and its files' keys, to the remote (Sol's review
// of rc.2).
func TestAPendingSendWithFilesIsGivenUpWhenSendFilesGoesOff(t *testing.T) {
	t.Parallel()
	for _, later := range []string{"its retry", "a later send"} {
		t.Run(later, func(t *testing.T) {
			t.Parallel()
			d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
			d.pageSend.Store(true)
			ctx := context.Background()
			before := d.sendFirst(t)
			d.writeMu.Lock()
			d.testPush = func(func() error) error { return errors.New("the connection dropped") }
			d.writeMu.Unlock()
			from := bus.Addr(d.device, "bbbbbb")
			r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "a file"}, []sendFile{{name: "one.txt", data: []byte("hello")}})
			if r.Error != "" {
				t.Fatal(r.Error)
			}
			d.pageSend.Store(false)
			d.writeMu.Lock()
			d.testPush = nil
			if later == "its retry" {
				d.retryPending()
			}
			d.writeMu.Unlock()
			if later == "a later send" {
				if err := d.appendEvent(ctx, message(d, "text alone")); err != nil {
					t.Fatal(err)
				}
			}
			tip, err := d.repo.RemoteRef(ctx, d.meshRef())
			if err != nil {
				t.Fatal(err)
			}
			if later == "its retry" && tip != before {
				t.Error("the pending message with files was pushed after Send files went off")
			}
			if later == "a later send" {
				if parent := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-parse", tip+"^")); parent != before {
					t.Error("a later send carried the pending message with files to the remote")
				}
			}
			d.writeMu.Lock()
			pending := len(d.pendSends)
			d.writeMu.Unlock()
			d.mu.Lock()
			inbox := d.st.Inbox["bbbbbb"]
			d.mu.Unlock()
			if pending != 0 || len(inbox) != 1 || !strings.Contains(inbox[0].Event.Body, "given up: sending files was turned off") {
				t.Errorf("%d still pending; the sender's inbox: %+v", pending, inbox)
			}
		})
	}
}

// A send of the same files as a pending one takes its ref, rather than uploading them again and
// being charged for them twice (#793, Sol's review of rc.2).
func TestASendOfTheSameFilesTakesAPendingOnesRef(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
	d.pageSend.Store(true)
	ctx := context.Background()
	d.sendFirst(t)
	d.writeMu.Lock()
	d.testPush = func(func() error) error { return errors.New("the connection dropped") }
	d.writeMu.Unlock()
	from := bus.Addr(d.device, "bbbbbb")
	files := []sendFile{{name: "one.txt", data: []byte("hello")}}
	if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "first"}, files); r.Error != "" {
		t.Fatal(r.Error)
	}
	done := make(chan proto.Response, 1)
	go func() { done <- d.sendWith(ctx, from, proto.Request{To: "*", Subject: "second"}, files) }()
	select {
	case r := <-done:
		if r.Error != "" {
			t.Fatalf("the second send: %s", r.Error)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the second send waited on the pending one")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := len(d.st.Uploads); n != 1 {
		t.Errorf("%d uploads counted, want 1: %+v", n, d.st.Uploads)
	}
}
