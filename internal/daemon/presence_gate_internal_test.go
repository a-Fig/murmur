package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A writer that comes to the gate after a presence has taken it, while that presence is still being
// encoded or waiting for the push slot, waits for it as one that came earlier does. The gate was let
// go the moment the presence was made, so such a writer pushed ahead of it.
func TestPresenceGateHoldsWhileSending(t *testing.T) {
	d := &Daemon{}
	gate := make(chan struct{})
	d.presenceFirst = gate

	// What publishPresence does as it makes the presence, under d.mu.
	d.mu.Lock()
	d.presenceFirst, d.presenceSending = nil, gate
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := d.awaitPresence(ctx); err == nil {
		t.Fatal("a writer went ahead of the presence being sent")
	}

	// A session starts meanwhile; its writer waits for the presence after this one too.
	next := make(chan struct{})
	d.mu.Lock()
	d.presenceFirst = next
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- d.awaitPresence(context.Background()) }()

	d.presenceSent(gate)
	select {
	case err := <-done:
		t.Fatalf("the writer went before the next presence: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if d.presenceSending != nil {
		t.Error("presenceSending is left set after its presence was sent")
	}
	close(next)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the writer is still waiting after both presences")
	}
	if err := d.awaitPresence(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The same, through publishPresence itself: while its push is held at the remote, a writer that
// comes to the gate waits, and once the push is done the gate is let go and cleared. A presence that
// fails lets go of its gate too.
func TestPublishPresenceHoldsItsGateUntilPushed(t *testing.T) {
	t.Parallel()
	d, _, clone := rateRig(t, Options{}, false)
	remote := filepath.Join(filepath.Dir(clone), "remote.git")
	hold := "#!/bin/sh\ncat >/dev/null\nsleep 2\nexit 0\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hold), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	d.mu.Lock()
	d.presenceFirst = gate
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- d.publishPresence(context.Background()) }()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		d.mu.Lock()
		taken := d.presenceSending == gate
		d.mu.Unlock()
		if taken {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the presence never took the gate")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := d.awaitPresence(ctx); err == nil {
		t.Fatal("a writer went ahead of the presence being pushed")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := d.awaitPresence(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	left := d.presenceSending
	d.mu.Unlock()
	if left != nil {
		t.Error("presenceSending is left set after its presence was pushed")
	}

	failed := make(chan struct{})
	d.mu.Lock()
	d.presenceFirst = failed
	d.mu.Unlock()
	refuse := "#!/bin/sh\ncat >/dev/null\nexit 1\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(refuse), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.publishPresence(context.Background()); err == nil {
		t.Fatal("a presence the remote refused was pushed")
	}
	select {
	case <-failed:
	default:
		t.Fatal("a presence that failed kept its gate shut")
	}
}
