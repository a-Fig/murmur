package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/attrib"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// The snapshots of tool calls in flight are bounded: a call whose end never comes (it was
// cancelled, or its hook died) must not leave one behind for good.

func bracketDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	d := &Daemon{opt: Options{GitDir: filepath.Join(dir, ".git"), StoppedInbox: time.Hour}, dir: t.TempDir(),
		log: log.New(io.Discard, "", 0), device: "here-0001", started: time.Now(), sessions: map[string]*session{},
		brackets: map[string]*bracket{},
		st:       state{Inbox: map[string][]*inMsg{}, Stopped: map[string]time.Time{}, Commits: map[string][]commitRec{}}}
	d.mu.Lock()
	d.newSessionLocked(&proto.Hello{ID: "aaaaaa", ClaudeSession: "claude-x", Cwd: dir}, "claude-code", "", "aaaaaa")
	d.mu.Unlock()
	return d, dir
}

func (d *Daemon) startReq(dir, toolUse string) proto.Request {
	return proto.Request{Op: proto.OpToolStart, ClaudeSession: "claude-x", ToolUseID: toolUse, Cwd: dir, GitDir: d.opt.GitDir}
}

func TestStartedCallsAreCapped(t *testing.T) {
	t.Parallel()
	d, dir := bracketDaemon(t)
	now := time.Now()
	d.mu.Lock()
	for i := range maxBrackets {
		// The most recently started has the highest i; "old-0" is the oldest.
		d.brackets["claude-x/old-"+strconv.Itoa(i)] = &bracket{cwd: dir, before: attrib.Ref{}, at: now.Add(-time.Duration(maxBrackets-i) * time.Second)}
	}
	d.mu.Unlock()

	d.toolStart(context.Background(), d.startReq(dir, "new"))

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.brackets["claude-x/new"]; !ok {
		t.Fatal("the new call has no snapshot")
	}
	if len(d.brackets) > maxBrackets {
		t.Errorf("%d snapshots held, the cap is %d", len(d.brackets), maxBrackets)
	}
	if _, ok := d.brackets["claude-x/old-0"]; ok {
		t.Error("the oldest snapshot was kept over the new one")
	}
	if _, ok := d.brackets["claude-x/old-1"]; !ok {
		t.Error("a snapshot was dropped that was not the oldest")
	}
}

// A snapshot whose call never ended is dropped once it is older than bracketTTL, well short of the
// cap.
func TestStartedCallsExpire(t *testing.T) {
	t.Parallel()
	d, dir := bracketDaemon(t)
	now := time.Now()
	d.mu.Lock()
	d.brackets["claude-x/ancient"] = &bracket{cwd: dir, at: now.Add(-bracketTTL - time.Minute)}
	d.brackets["claude-x/running"] = &bracket{cwd: dir, at: now.Add(-bracketTTL + time.Minute)}
	d.mu.Unlock()

	d.toolStart(context.Background(), d.startReq(dir, "new"))

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.brackets["claude-x/ancient"]; ok {
		t.Errorf("a snapshot older than %v was kept", bracketTTL)
	}
	if _, ok := d.brackets["claude-x/running"]; !ok {
		t.Errorf("a snapshot younger than %v was dropped", bracketTTL)
	}
}

// A call that ends leaves no snapshot behind, whether or not it made a commit.
func TestEndedCallsLeaveNoSnapshot(t *testing.T) {
	t.Parallel()
	d, dir := bracketDaemon(t)
	held := func() int {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.brackets)
	}
	d.toolStart(context.Background(), d.startReq(dir, "t1"))
	if held() != 1 {
		t.Fatalf("%d snapshots after a start, want 1", held())
	}
	end := d.startReq(dir, "t1")
	end.Op = proto.OpToolEnd
	d.toolEnd(context.Background(), end)
	if n := held(); n != 0 {
		t.Errorf("%d snapshots after the call ended, want 0", n)
	}
}

// What the hook sends that the daemon can't pair or place leaves nothing behind.
func TestStartsThatCannotBePairedAreNotKept(t *testing.T) {
	t.Parallel()
	d, dir := bracketDaemon(t)
	for name, req := range map[string]proto.Request{
		"no tool_use_id":   {Op: proto.OpToolStart, ClaudeSession: "claude-x", Cwd: dir, GitDir: d.opt.GitDir},
		"no cwd":           {Op: proto.OpToolStart, ClaudeSession: "claude-x", ToolUseID: "t", GitDir: d.opt.GitDir},
		"another clone":    {Op: proto.OpToolStart, ClaudeSession: "claude-x", ToolUseID: "t", Cwd: dir, GitDir: t.TempDir()},
		"no clone":         {Op: proto.OpToolStart, ClaudeSession: "claude-x", ToolUseID: "t", Cwd: dir},
		"no session":       {Op: proto.OpToolStart, ClaudeSession: "claude-y", ToolUseID: "t", Cwd: dir, GitDir: d.opt.GitDir},
		"a dead directory": {Op: proto.OpToolStart, ClaudeSession: "claude-x", ToolUseID: "t", Cwd: filepath.Join(dir, "gone"), GitDir: d.opt.GitDir},
	} {
		if resp := d.toolStart(context.Background(), req); !resp.OK {
			t.Errorf("%s: the hook got an error: %q", name, resp.Error)
		}
		d.mu.Lock()
		n := len(d.brackets)
		d.mu.Unlock()
		if n != 0 {
			t.Errorf("%s: %d snapshots kept", name, n)
		}
	}
}

// A state.json written before commits were kept has no "commits" key; loading it must leave a map
// to credit into, as a write to a nil map would panic the daemon on the first commit.
func TestStateFromBeforeCommitsLoads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := `{"processed":{},"inbox":{},"stopped":{},"usedNames":{"quiet-otter":"s1"}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{dir: dir, log: log.New(io.Discard, "", 0)}
	if err := d.loadState(); err != nil {
		t.Fatal(err)
	}
	if d.st.Commits == nil || d.st.Used["quiet-otter"] != "s1" {
		t.Fatalf("loaded %+v", d.st)
	}
	s := &session{id: "s1"}
	d.mu.Lock()
	d.creditLocked([]*session{s}, []attrib.Commit{{SHA: "abc", Branch: "main"}}, time.Now())
	d.mu.Unlock()
	if got := d.st.Commits["s1"]; len(got) != 1 || got[0].SHA != "abc" {
		t.Errorf("credited %+v", got)
	}
}

// Another device's last-active time is worked out from the gap its presence states between writing
// and the activity, taken back from when this daemon saw the presence change, so the two machines'
// clocks never meet.
func TestPeerActiveIsTheWritersGapOnThisClock(t *testing.T) {
	t.Parallel()
	seen := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	// The writer's clock is three hours ahead of this one.
	wrote := seen.Add(3 * time.Hour).Add(-2 * time.Second)
	p := &peer{lastChange: seen}
	p.pres.At = wrote
	active := wrote.Add(-90 * time.Second)

	got := peerActive(p, bus.SessionInfo{LastActive: active})
	if want := seen.Add(-90 * time.Second); !got.Equal(want) {
		t.Errorf("active %v, want %v: the 90 s gap taken back from when the presence was seen", got, want)
	}
	// An activity time after the write (clocks stepping) is no later than the write.
	if got := peerActive(p, bus.SessionInfo{LastActive: wrote.Add(time.Minute)}); !got.Equal(seen) {
		t.Errorf("active %v, want %v for a time after the write", got, seen)
	}
	if got := peerActive(p, bus.SessionInfo{}); !got.IsZero() {
		t.Errorf("a presence without the time gave %v", got)
	}
	if got := peerActive(&peer{lastChange: seen}, bus.SessionInfo{LastActive: active}); !got.IsZero() {
		t.Errorf("a presence without its own time gave %v", got)
	}
}

// A peer session's start is worked out as its last activity is, so a start dated after the presence
// that lists it, by a clock ahead or a presence written so, is no later than when the presence was
// seen, and a session that says nothing of its activity can't sort above the rest of `who` for it.
func TestPeerStartedIsNoLaterThanThePresence(t *testing.T) {
	seen := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	wrote := seen.Add(3 * time.Hour) // the writer's clock is three hours ahead
	p := &peer{lastChange: seen}
	p.pres.At = wrote
	if got, want := peerStarted(p, bus.SessionInfo{Started: wrote.Add(-time.Hour)}), seen.Add(-time.Hour); !got.Equal(want) {
		t.Errorf("started %v, want %v: the hour's gap taken back from when the presence was seen", got, want)
	}
	if got := peerStarted(p, bus.SessionInfo{Started: wrote.Add(24 * time.Hour)}); !got.Equal(seen) {
		t.Errorf("a start after the presence's write gave %v, want %v", got, seen)
	}
	if got := peerStarted(p, bus.SessionInfo{}); !got.IsZero() {
		t.Errorf("a session without a start gave %v", got)
	}
	noAt := &peer{lastChange: seen}
	if got := peerStarted(noAt, bus.SessionInfo{Started: seen.Add(time.Hour)}); !got.Equal(seen) {
		t.Errorf("without the presence's own time, a later start gave %v, want %v", got, seen)
	}
	if got := peerStarted(noAt, bus.SessionInfo{Started: seen.Add(-time.Hour)}); !got.Equal(seen.Add(-time.Hour)) {
		t.Errorf("without the presence's own time, an earlier start gave %v", got)
	}
}

// A presence this daemon has never seen change is dated from when it was written, and never from
// later than now, so a writer's clock that runs ahead can't make a device look newer than it is.
func TestFirstSightIsWhenItWasWrittenButNeverLater(t *testing.T) {
	// Not parallel: it allows a second between two readings of the clock, which other tests'
	// load could stretch.
	near := func(got time.Time, ago time.Duration) bool {
		return time.Since(got.Add(ago)) < time.Second && time.Since(got.Add(ago)) > -time.Second
	}
	if got := firstSight(time.Now().Add(-5 * time.Hour)); !near(got, 5*time.Hour) {
		t.Errorf("a presence written 5 hours ago is dated %v ago", time.Since(got))
	}
	if got := firstSight(time.Now().Add(time.Hour)); !near(got, 0) {
		t.Errorf("a presence written in the future is dated %v ago, want now", time.Since(got))
	}
}

// A stopped session's commits are kept exactly as long as its inbox: both go in the same sweep,
// and a session of that ID that registers after the time is up starts with neither. (Worked out
// on the daemon's own state, as the keep time in a running daemon is a race against how slow git
// is on the machine.)
func TestCreditedCommitsExpireWithTheInbox(t *testing.T) {
	t.Parallel()
	d, _ := bracketDaemon(t)
	now := time.Now()
	stop := func(ago time.Duration) {
		d.mu.Lock()
		defer d.mu.Unlock()
		s := d.sessions["aaaaaa"]
		if s != nil {
			d.creditLocked([]*session{s}, []attrib.Commit{{SHA: "c1", Branch: "main"}}, now)
			delete(d.sessions, "aaaaaa")
		}
		d.st.Stopped["aaaaaa"] = now.Add(-ago)
		d.st.Inbox["aaaaaa"] = []*inMsg{{}}
	}
	kept := func() (inbox, commits bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.st.Inbox["aaaaaa"]) > 0, len(d.st.Commits["aaaaaa"]) > 0
	}

	stop(30 * time.Minute) // the keep time is an hour
	d.pruneInboxes()
	if inbox, commits := kept(); !inbox || !commits {
		t.Fatalf("inbox kept %v, commits kept %v, 30 minutes into an hour", inbox, commits)
	}
	stop(2 * time.Hour)
	d.pruneInboxes()
	if inbox, commits := kept(); inbox || commits {
		t.Errorf("inbox kept %v, commits kept %v, two hours after the stop", inbox, commits)
	}

	// Expired but not yet swept, and the session comes back: it doesn't inherit the commits.
	d.mu.Lock()
	d.creditLocked([]*session{{id: "aaaaaa"}}, []attrib.Commit{{SHA: "c2", Branch: "main"}}, now)
	d.st.Stopped["aaaaaa"] = now.Add(-2 * time.Hour)
	d.newSessionLocked(&proto.Hello{ID: "aaaaaa", ClaudeSession: "claude-x", Cwd: "."}, "claude-code", "", "aaaaaa")
	left := len(d.st.Commits["aaaaaa"])
	d.mu.Unlock()
	if left != 0 {
		t.Errorf("a session back after its inbox expired has %d commits", left)
	}

	// Commits of a session that is neither running nor stopped are no one's, once the daemon has
	// been up long enough to know no one is coming back for them.
	d.mu.Lock()
	d.st.Commits["ghost"] = []commitRec{{SHA: "c3"}}
	d.started = now.Add(-3 * time.Minute)
	d.mu.Unlock()
	d.pruneInboxes()
	d.mu.Lock()
	_, ghost := d.st.Commits["ghost"]
	d.mu.Unlock()
	if ghost {
		t.Error("the commits of a session that is neither running nor stopped were kept")
	}
}

// A commit is written to state.json as soon as it is credited, not at the next stop: a daemon that
// is killed (a crash, a reboot) would otherwise lose what it had credited since it last wrote.
func TestCreditedCommitIsSavedAtOnce(t *testing.T) {
	t.Parallel()
	d, dir := bracketDaemon(t)
	ctx := context.Background()
	d.toolStart(ctx, d.startReq(dir, "t1"))
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "during the call")
	sha := run("rev-parse", "HEAD")
	end := d.startReq(dir, "t1")
	end.Op = proto.OpToolEnd
	d.toolEnd(ctx, end)
	d.settleEnds()

	b, err := os.ReadFile(d.statePath())
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Commits map[string][]commitRec `json:"commits"`
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if got := saved.Commits["aaaaaa"]; len(got) != 1 || got[0].SHA != sha || got[0].Branch != "main" {
		t.Errorf("state.json has %+v after the call, want %s on main", got, sha)
	}
}

// The end of a call is answered before its commits are found: the hook, and with it the agent and
// the notices the hook brings, doesn't wait for git. What it committed is credited all the same, and
// a lookup waits for it.
func TestCallEndIsAnsweredBeforeGit(t *testing.T) {
	// Not parallel: it replaces snapshot, which every daemon in the process calls.
	d, dir := bracketDaemon(t)
	ctx := context.Background()
	d.toolStart(ctx, d.startReq(dir, "t1"))
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "during")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	release := make(chan struct{})
	orig := snapshot
	snapshot = func(ctx context.Context, wt string) (attrib.Ref, error) {
		<-release
		return orig(ctx, wt)
	}
	defer func() { snapshot = orig }()
	end := d.startReq(dir, "t1")
	end.Op = proto.OpToolEnd
	answered := make(chan struct{})
	go func() {
		d.toolEnd(ctx, end)
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(3 * time.Second):
		close(release)
		<-answered
		t.Fatal("the end of a call was answered only once its snapshot was taken")
	}
	close(release)
	d.settleEnds()
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.st.Commits["aaaaaa"]; len(got) != 1 || got[0].Subject != "during" {
		t.Errorf("credited %+v, want the commit made during the call", got)
	}
}

func igit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The end of a call is snapshotted after it is answered, so the next call to start waits for that
// snapshot: Claude Code runs it as soon as the end is answered, and one that switches branches
// before the snapshot would hide what the first call committed.
func TestNextCallWaitsForTheEndBeforeIt(t *testing.T) {
	// Not parallel: it replaces snapshot, which every daemon in the process calls.
	d, dir := bracketDaemon(t)
	ctx := context.Background()
	d.toolStart(ctx, d.startReq(dir, "t1"))
	igit(t, dir, "switch", "-q", "-c", "feat")
	igit(t, dir, "commit", "-q", "--allow-empty", "-m", "on feat")

	release := make(chan struct{})
	orig := snapshot
	snapshot = func(ctx context.Context, wt string) (attrib.Ref, error) {
		<-release
		return orig(ctx, wt)
	}
	defer func() { snapshot = orig }()
	end := d.startReq(dir, "t1")
	end.Op = proto.OpToolEnd
	d.toolEnd(ctx, end)
	// The snapshot is let go a little after the next call asks to start: that call can't start
	// before it.
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(release)
	}()
	d.toolStart(ctx, d.startReq(dir, "t2"))
	igit(t, dir, "switch", "-q", "main") // what the second call does
	end2 := d.startReq(dir, "t2")
	end2.Op = proto.OpToolEnd
	d.toolEnd(ctx, end2)
	d.settleEnds()

	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.st.Commits["aaaaaa"]; len(got) != 1 || got[0].Subject != "on feat" || got[0].Branch != "feat" {
		t.Errorf("credited %+v, want the first call's commit, on feat", got)
	}
}

// Calls are judged in the background, and one can be judged after a call that ended later. What
// each credited is kept in the order the calls ended, so the branches a session committed to are
// listed most recent first all the same.
func TestCreditsKeepTheOrderCallsEnded(t *testing.T) {
	// Not parallel: it replaces snapshot, which every daemon in the process calls.
	d, dir := bracketDaemon(t)
	ctx := context.Background()
	wt2 := filepath.Join(t.TempDir(), "two")
	igit(t, dir, "worktree", "add", "-q", "-b", "feat-b", wt2)
	d.toolStart(ctx, d.startReq(dir, "t1"))
	d.toolStart(ctx, d.startReq(wt2, "t2"))

	release := make(chan struct{})
	orig := snapshot
	snapshot = func(ctx context.Context, wt string) (attrib.Ref, error) {
		if wt == dir {
			<-release
		}
		return orig(ctx, wt)
	}
	defer func() { snapshot = orig }()
	igit(t, dir, "switch", "-q", "-c", "feat-a")
	igit(t, dir, "commit", "-q", "--allow-empty", "-m", "a")
	end1 := d.startReq(dir, "t1")
	end1.Op = proto.OpToolEnd
	d.toolEnd(ctx, end1)
	igit(t, wt2, "commit", "-q", "--allow-empty", "-m", "b")
	end2 := d.startReq(wt2, "t2")
	end2.Op = proto.OpToolEnd
	d.toolEnd(ctx, end2)
	for deadline := time.Now().Add(20 * time.Second); ; {
		d.mu.Lock()
		n := len(d.st.Commits["aaaaaa"])
		d.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("the second call's commit wasn't credited")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	d.settleEnds()

	d.mu.Lock()
	defer d.mu.Unlock()
	var got []string
	for _, r := range d.st.Commits["aaaaaa"] {
		got = append(got, r.Subject)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("credited %v, want [a b], in the order the calls ended", got)
	}
	if b := d.committedLocked("aaaaaa"); strings.Join(b, ",") != "feat-b,feat-a" {
		t.Errorf("committed to %v, want [feat-b feat-a], most recent first", b)
	}
}

// A panic while crediting a call's commits is logged and costs only that call's credit: it
// doesn't leave mu held, which hung the daemon, and its tests, with nothing in the log (#467).
func TestAPanicWhileCreditingIsLoggedAndReleasesTheLock(t *testing.T) {
	t.Parallel()
	d, dir := bracketDaemon(t)
	var logged strings.Builder
	d.log = log.New(&logged, "", 0)
	ctx := context.Background()
	d.toolStart(ctx, d.startReq(dir, "t1"))
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "during the call")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	d.mu.Lock()
	d.st.Commits = nil // creditLocked's write to it panics, as #451's nil map did
	d.mu.Unlock()
	end := d.startReq(dir, "t1")
	end.Op = proto.OpToolEnd
	d.toolEnd(ctx, end)

	settled := make(chan struct{})
	go func() {
		d.settleEnds()
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon still holds mu 30s after the call ended: the panic left it locked")
	}
	d.mu.Lock()
	ending := d.ending
	d.mu.Unlock()
	if ending != 0 {
		t.Errorf("ending = %d after the panicked call, want 0", ending)
	}
	if got := logged.String(); !strings.Contains(got, "panic while crediting") || !strings.Contains(got, "assignment to entry in nil map") {
		t.Errorf("log = %q, want the panic and its message", got)
	}
}
