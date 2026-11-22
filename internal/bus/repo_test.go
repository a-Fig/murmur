package bus_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// IsCommit answers only for what it can tell: a commit, or not one (a tree, an object that isn't
// here). A git that fails otherwise gives no answer, since callers build over what isn't a commit.
func TestIsCommit(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ctx := context.Background()
	r := &bus.Repo{Git: gitx.Git{Dir: dir}, Name: "test", Email: "test@example.invalid"}
	commit, err := r.NewCommit(ctx, "", "a commit")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := r.EmptyTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what, sha string
		want      bool
	}{{"a commit", commit, true}, {"a tree", tree, false}, {"a missing object", strings.Repeat("0", 39) + "1", false}} {
		if got, err := r.IsCommit(ctx, c.sha); err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.what, got, err, c.want)
		}
	}
	broken := &bus.Repo{Git: gitx.Git{Dir: filepath.Join(dir, "nowhere")}}
	if got, err := broken.IsCommit(ctx, commit); err == nil {
		t.Errorf("a git that can't run answered %v", got)
	}

	// The same through a git that is kept between reads.
	kept := &bus.Repo{Git: r.Git}
	kept.KeepReader()
	t.Cleanup(kept.Close)
	for _, c := range []struct {
		what, sha string
		want      bool
	}{{"a commit", commit, true}, {"a tree", tree, false}, {"a missing object", strings.Repeat("0", 39) + "1", false}} {
		if got, err := kept.IsCommit(ctx, c.sha); err != nil || got != c.want {
			t.Errorf("a kept git, %s: got %v, %v; want %v", c.what, got, err, c.want)
		}
	}
	broken.KeepReader()
	t.Cleanup(broken.Close)
	if got, err := broken.IsCommit(ctx, commit); err == nil {
		t.Errorf("a kept git that can't run answered %v", got)
	}
}

// A leased push is a compare-and-swap: it lands only if the remote ref holds what it expects. Of
// two racing on the same expectation, exactly one lands, and the other is told ErrRejected,
// whether git refused it before sending or the server did once the other had landed.
func TestLeasePush(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	ctx := context.Background()
	ref := brand.RefRoot + "lease"
	repos := make([]*bus.Repo, 2)
	for i := range repos {
		dir := filepath.Join(root, fmt.Sprintf("local%d.git", i))
		for _, d := range []string{remote, dir} {
			if out, err := exec.Command("git", "init", "-q", "--bare", d).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}
		}
		repos[i] = &bus.Repo{Git: gitx.Git{Dir: dir}, Remote: remote, Name: "test", Email: "test@example.invalid"}
	}
	commit := func(r *bus.Repo, msg string) string {
		t.Helper()
		sha, err := r.NewCommit(ctx, "", msg)
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}

	// Expecting no ref: lands when there is none, and is refused once there is.
	tip := commit(repos[0], "first")
	if err := repos[0].Push(ctx, tip, ref, bus.Lease, ""); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := repos[1].Push(ctx, commit(repos[1], "also first"), ref, bus.Lease, ""); !errors.Is(err, bus.ErrRejected) {
		t.Fatalf("push expecting no ref, over one: %v", err)
	}

	for round := range 5 {
		shas, errs := make([]string, len(repos)), make([]error, len(repos))
		for i, r := range repos {
			shas[i] = commit(r, fmt.Sprintf("round %d, pusher %d", round, i))
		}
		var wg sync.WaitGroup
		for i, r := range repos {
			wg.Go(func() { errs[i] = r.Push(ctx, shas[i], ref, bus.Lease, tip) })
		}
		wg.Wait()
		won := -1
		for i, err := range errs {
			switch {
			case err == nil && won >= 0:
				t.Fatalf("round %d: both pushes landed", round)
			case err == nil:
				won = i
			case !errors.Is(err, bus.ErrRejected):
				t.Fatalf("round %d, pusher %d: %v", round, i, err)
			}
		}
		if won < 0 {
			t.Fatalf("round %d: neither push landed", round)
		}
		if now, err := repos[0].RemoteRef(ctx, ref); err != nil || now != shas[won] {
			t.Fatalf("round %d: the remote holds %s (%v), want the winner's %s", round, now, err, shas[won])
		}
		tip = shas[won]
	}
}

// One Repo serves all of a daemon's goroutines, and the first commits can come at once (the
// presence loop, a session's first event). Under -race this catches an unguarded cache.
func TestNewCommitConcurrently(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	r := &bus.Repo{Git: gitx.Git{Dir: dir}, Name: "test", Email: "test@example.invalid"}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := r.NewCommit(context.Background(), "", "x"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// A fetch tells the server only of the bus commits the clone has, never of the user's own
// history, which no bus commit shares. A presence commit has no parent, so the server finds
// nothing in common with it, and a clone offering its branches would list every commit on them,
// a round trip for each batch.
func TestFetchOffersOnlyBusCommits(t *testing.T) {
	root := t.TempDir()
	remote, local, peer := filepath.Join(root, "remote.git"), filepath.Join(root, "local.git"), filepath.Join(root, "peer.git")
	for _, d := range []string{remote, local, peer} {
		if out, err := exec.Command("git", "init", "-q", "--bare", d).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
	}
	// The user's own history: 100 commits the remote has never seen, written in one process.
	var stream strings.Builder
	for i := 1; i <= 100; i++ {
		msg := fmt.Sprintf("work %d", i)
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter u <u@example.invalid> %d +0000\ndata %d\n%s\n",
			1700000000+i, len(msg), msg)
	}
	imp := exec.Command("git", "--git-dir="+local, "fast-import", "--quiet")
	imp.Stdin = strings.NewReader(stream.String())
	if out, err := imp.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v: %s", err, out)
	}
	out, err := exec.Command("git", "--git-dir="+local, "rev-list", "refs/heads/main").Output()
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]bool{}
	for _, sha := range strings.Fields(string(out)) {
		mine[sha] = true
	}
	if len(mine) != 100 {
		t.Fatalf("the user's branch has %d commits, want 100", len(mine))
	}

	ctx := context.Background()
	trace := filepath.Join(root, "packets")
	r := &bus.Repo{Git: gitx.Git{Dir: local, Env: []string{"GIT_TRACE_PACKET=" + trace}}, Remote: remote}
	p := &bus.Repo{Git: gitx.Git{Dir: peer}, Remote: remote, Name: "peer", Email: "peer@example.invalid"}
	push := func(ref, parent, msg string, mode bus.PushMode) string {
		t.Helper()
		sha, err := p.NewCommit(ctx, parent, msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Push(ctx, sha, ref, mode, ""); err != nil {
			t.Fatal(err)
		}
		return sha
	}
	// fetch fetches ref and returns the commits it offered the server: how many of the user's,
	// and which others.
	fetch := func(ref, want string) (user int, other map[string]bool) {
		t.Helper()
		os.Remove(trace)
		if err := r.Fetch(ctx, []string{ref}); err != nil {
			t.Fatalf("fetch %s: %v", ref, err)
		}
		if got, err := r.Git.Run(ctx, "", "rev-parse", ref); err != nil || strings.TrimSpace(got) != want {
			t.Fatalf("after the fetch %s is %q (%v), want %s", ref, got, err, want)
		}
		packets, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		other = map[string]bool{}
		for _, line := range strings.Split(string(packets), "\n") {
			_, sha, ok := strings.Cut(line, "fetch> have ")
			sha = strings.TrimSpace(sha)
			switch {
			case !ok:
			case mine[sha]:
				user++
			default:
				other[sha] = true
			}
		}
		return user, other
	}
	presence, mesh := brand.PresencePrefix+"peer", brand.MeshPrefix+"peer"

	// A clone with no bus refs yet offers nothing.
	sha := push(presence, "", "presence 1", bus.Force)
	if user, other := fetch(presence, sha); user+len(other) != 0 {
		t.Errorf("a clone with no bus refs offered %d of the user's commits and %d others, want none", user, len(other))
	}
	// One that has some offers those, and still none of the user's.
	first := push(mesh, "", "a log", bus.FastForward)
	fetch(mesh, first)
	sha = push(presence, "", "presence 2", bus.Force)
	user, other := fetch(presence, sha)
	if user != 0 {
		t.Errorf("fetching a presence commit offered %d of the user's commits, want none", user)
	}
	if len(other) == 0 {
		t.Error("fetching a presence commit offered none of the clone's bus commits")
	}
	// A log's next commit: the clone offers its copy of the log, so the server sends only what is
	// new rather than the whole log again.
	sha = push(mesh, first, "the log goes on", bus.FastForward)
	user, other = fetch(mesh, sha)
	if user != 0 {
		t.Errorf("fetching a log's next commit offered %d of the user's commits, want none", user)
	}
	if !other[first] {
		t.Errorf("fetching a log's next commit offered %v, not the clone's copy of the log, %s", other, first)
	}
}

// Bus traffic leaves the user's branches and hooks alone (#950). The clone here has the remote's
// fetch refspecs map bus refs onto branches, and a reference-transaction hook, which git runs on
// every ref it writes. Without --refmap= a fetch would move those branches, and a push would move
// or delete them as it set what it took for their remote-tracking copies; and every fetch, push and
// update-ref would run the hook. Each bus ref is also made a symbolic ref to the checked-out branch
// before it is written (#965): git writes through one, so a fetch or an update-ref would move main
// to the bus commit, and a delete would delete main.
func TestBusLeavesBranchesAndHooksAlone(t *testing.T) {
	root := t.TempDir()
	remote, clone := filepath.Join(root, "remote.git"), filepath.Join(root, "clone")
	ran := filepath.Join(root, "hook-ran")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", clone, "-c", "user.name=u", "-c", "user.email=u@example.invalid"}, args...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	for _, args := range [][]string{{"init", "-q", "--bare", remote}, {"init", "-q", "-b", "main", clone}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
	}
	git("commit", "-q", "--allow-empty", "-m", "work")
	git("branch", "side")
	git("remote", "add", "origin", remote)
	git("config", "--add", "remote.origin.fetch", "+"+brand.RefRoot+"mesh/dev:refs/heads/side")
	git("config", "--add", "remote.origin.fetch", "+"+brand.RefRoot+"*:refs/heads/stolen/*")
	// Every ref outside the bus, compared at the end: branches, remote-tracking refs, tags.
	notBus := func() string {
		t.Helper()
		var keep []string
		for _, line := range strings.Split(git("for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
			if !strings.HasPrefix(line, brand.RefRoot) {
				keep = append(keep, line)
			}
		}
		return strings.Join(keep, "\n")
	}
	before := notBus()
	hooks := filepath.Join(clone, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho \"$1\" >> '" + filepath.ToSlash(ran) + "'\n"
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	// The hook runs as set up, so its silence below is Murmur's doing.
	git("update-ref", "refs/heads/probe", "HEAD")
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the hook didn't run for git itself: %v", err)
	}
	git("update-ref", "-d", "refs/heads/probe")
	if err := os.Remove(ran); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	r := &bus.Repo{Git: gitx.Git{Dir: filepath.Join(clone, ".git")}, Remote: "origin", Name: "test", Email: "test@example.invalid"}
	sha, err := r.NewCommit(ctx, "", "a message")
	if err != nil {
		t.Fatal(err)
	}
	dev, other := brand.RefRoot+"mesh/dev", brand.RefRoot+"mesh/other"
	for _, ref := range []string{dev, other} {
		if err := r.Push(ctx, sha, ref, bus.Force, ""); err != nil {
			t.Fatalf("push %s: %v", ref, err)
		}
	}
	// point makes each ref a symbolic ref to main, without the hook, which would count as Murmur's.
	main := git("rev-parse", "refs/heads/main")
	point := func(refs ...string) {
		t.Helper()
		for _, ref := range refs {
			git("-c", "core.hooksPath="+filepath.Join(root, "no-hooks"), "symbolic-ref", ref, "refs/heads/main")
		}
	}
	// symbolic says whether ref is still a symbolic ref; mainStays fails the test if main moved.
	symbolic := func(ref string) bool {
		return exec.Command("git", "-C", clone, "symbolic-ref", "-q", ref).Run() == nil
	}
	mainStays := func(op string) {
		t.Helper()
		if now, err := exec.Command("git", "-C", clone, "rev-parse", "--verify", "-q", "refs/heads/main").Output(); err != nil {
			t.Fatalf("%s through a symbolic bus ref deleted main: %v", op, err)
		} else if got := strings.TrimSpace(string(now)); got != main {
			t.Fatalf("%s through a symbolic bus ref moved main from %s to %s", op, main, got)
		}
	}
	point(dev, other)
	// A symbolic ref is no copy of a bus ref: nothing is built on main's commit.
	if local, err := r.LocalRefs(ctx); err != nil {
		t.Fatal(err)
	} else if s, ok := local[dev]; ok {
		t.Errorf("LocalRefs gave the symbolic %s as %s, main's commit", dev, s)
	}
	if err := r.Fetch(ctx, []string{dev, other}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	mainStays("a fetch")
	// A fetch that fails, of a ref the remote hasn't, leaves nothing either.
	if err := r.Fetch(ctx, []string{brand.RefRoot + "mesh/gone"}); err == nil {
		t.Error("fetching a ref the remote hasn't worked")
	}
	if left := git("for-each-ref", "--format=%(refname)", brand.RefRoot); strings.Contains(left, brand.RefRoot+"fetch-") {
		t.Errorf("fetches left their own refs:\n%s", left)
	}
	for _, ref := range []string{dev, other} {
		if symbolic(ref) {
			t.Errorf("the fetch left %s symbolic", ref)
		}
		if got := git("rev-parse", ref); got != sha {
			t.Errorf("the fetch set %s to %s, not the bus commit %s", ref, got, sha)
		}
	}
	point(other)
	if err := r.UpdateLocal(ctx, other, sha); err != nil {
		t.Fatal(err)
	}
	mainStays("an update")
	if symbolic(other) || git("rev-parse", other) != sha {
		t.Errorf("the update didn't set %s itself to %s", other, sha)
	}
	point(other)
	if err := r.DeleteLocal(ctx, other); err != nil {
		t.Fatal(err)
	}
	mainStays("a delete")
	if exec.Command("git", "-C", clone, "rev-parse", "--verify", "-q", other).Run() == nil {
		t.Errorf("the delete left %s", other)
	}
	point(dev)
	if err := r.Push(ctx, "", dev, bus.Force, ""); err != nil {
		t.Fatalf("push deleting %s: %v", dev, err)
	}
	if err := r.Push(ctx, sha, dev, bus.Lease, ""); err != nil {
		t.Fatalf("leased push %s: %v", dev, err)
	}
	// A server can say it took a push under another name (proc-receive's "option refname"), and
	// git would update whatever tracks that name: here refs/remotes/origin/evil.
	renamed := brand.RefRoot + "renamed"
	report := `#!/bin/sh
size() {
	size=$(dd bs=1 count=4 2>/dev/null)
	[ -n "$size" ] && [ "$size" != 0000 ]
}
data() { line=$(dd bs=1 count=$((0x$size - 4)) 2>/dev/null); }
skip() { dd bs=1 count=$((0x$size - 4)) >/dev/null 2>&1; }
say() { printf '%04x%s' $((${#1} + 4)) "$1"; }
while size; do skip; done
say "version=1"
printf 0000
refs=
while size; do
	data
	set -- $line
	refs="$refs $3"
done
for r in $refs; do say "ok $r"; say "option refname refs/heads/evil"; done
printf 0000
`
	if err := os.MkdirAll(filepath.Join(remote, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "hooks", "proc-receive"), []byte(report), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "--git-dir="+remote, "config", "receive.procReceiveRefs", renamed).CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, out)
	}
	if err := r.Push(ctx, sha, renamed, bus.Force, ""); err != nil {
		t.Fatalf("push %s, reported as refs/heads/evil: %v", renamed, err)
	}

	mainStays("a push")
	if after := notBus(); after != before {
		t.Errorf("bus traffic changed refs outside %s; before:\n%s\nafter:\n%s", brand.RefRoot, before, after)
	}
	if b, err := os.ReadFile(ran); err == nil {
		t.Errorf("bus traffic ran the clone's reference-transaction hook:\n%s", b)
	}
}
