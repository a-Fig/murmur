package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/setup"
)

// `mur nickname` gives the clone's device a nickname (#190), says what it has, and takes it off.
func TestNickname(t *testing.T) {
	_, clone := cloneOf(t, t.TempDir())
	t.Cleanup(func() { stopDaemon(clone) })
	ctx := context.Background()
	if _, err := setup.Init(ctx, clone); err != nil {
		t.Fatal(err)
	}
	p, err := join.Locate(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runNickname(ctx, append([]string{"-C", clone}, args...), &out)
		return out.String(), err
	}
	if out, err := run("tyler"); err != nil || !strings.Contains(out, "has the nickname tyler") {
		t.Fatalf("setting it: %q, %v", out, err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "has the nickname tyler") {
		t.Errorf("asking what it is: %q, %v", out, err)
	}
	if _, err := run("Not A Nickname"); err == nil || !strings.Contains(err.Error(), "can't be a nickname") {
		t.Errorf("a name that can't be one: %v", err)
	}
	if _, err := run("tyler", "laptop"); err == nil {
		t.Error("two names were taken")
	}
	if out, err := run("--clear"); err != nil || !strings.Contains(out, "has no nickname.") {
		t.Errorf("taking it off: %q, %v", out, err)
	}
}
