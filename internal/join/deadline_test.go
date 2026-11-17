package join

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/join/jointest"
	"github.com/a-Fig/murmur/internal/publicrepo"
)

// A join stuck on a step gives up at its deadline and says which step, rather than running until the
// agent's own timeout kills it with nothing printed (#829). While it waits, it says what on.
func TestJoinGivesUpOnAStuckStepAndSaysWhich(t *testing.T) {
	wasWait, wasQuiet := joinWait, joinQuiet
	joinWait, joinQuiet = 2*time.Second, 50*time.Millisecond
	t.Cleanup(func() { joinWait, joinQuiet = wasWait, wasQuiet })
	sb := newSandbox(t, true)
	t.Setenv(jointest.HangEnv, "1")
	var said bytes.Buffer
	sb.Progress = &said
	p, _ := clone(t)

	start := time.Now()
	r, err := Join(context.Background(), sb.Env, p.Top)
	if took := time.Since(start); took > time.Minute {
		t.Errorf("the join took %v", took)
	}
	want := "gave up after 2 seconds, while adding " + brand.Product + " to Claude Code"
	if err == nil || len(r.Problems) != 1 || r.Problems[0] != want {
		t.Fatalf("err %v, problems %q; want only %q", err, r.Problems, want)
	}
	if !strings.Contains(said.String(), brand.Command+" join: still adding "+brand.Product+" to Claude Code\n") {
		t.Errorf("while waiting, said %q", said.String())
	}
	if p.Joined() {
		t.Error("the consent was recorded though the join stopped before it")
	}
	if len(r.Tell) != 1 || !strings.HasPrefix(r.Tell[0], "Joining didn't finish") {
		t.Errorf("tell %q", r.Tell)
	}
}

// A join cut short after the user joined still says that a daemon may report the repo, since one may.
func TestJoinCutShortAfterConsentKeepsThePublicRepoStatement(t *testing.T) {
	r, err := Env{}.cutShort(Report{}, context.Background(), "starting this clone's daemon", true)
	if err == nil || len(r.Tell) != 2 || r.Tell[0] != publicrepo.Statement {
		t.Errorf("err %v, tell %q", err, r.Tell)
	}
	if r, _ := (Env{}).cutShort(Report{}, context.Background(), "x", false); slices.Contains(r.Tell, publicrepo.Statement) {
		t.Errorf("before consent, tell %q", r.Tell)
	}
}
