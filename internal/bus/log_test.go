package bus_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

func logRepo(t *testing.T) *bus.Repo {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return &bus.Repo{Git: gitx.Git{Dir: dir}, Name: "test", Email: "test@example.invalid"}
}

// chain commits msgs one on another, and returns their SHAs, oldest first.
func chain(t *testing.T, r *bus.Repo, parent string, msgs ...string) []string {
	t.Helper()
	var shas []string
	for _, m := range msgs {
		sha, err := r.NewCommit(context.Background(), parent, m)
		if err != nil {
			t.Fatal(err)
		}
		shas, parent = append(shas, sha), sha
	}
	return shas
}

func readLog(t *testing.T, r *bus.Repo, from, to string, max int64) (pages [][]bus.Commit) {
	t.Helper()
	err := r.Log(context.Background(), from, to, max, func(p []bus.Commit) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pages
}

// A long history comes a page at a time, oldest first, each commit once and whole, and a read from
// the middle has only what came after.
func TestLogReadsALongHistoryInPages(t *testing.T) {
	r := logRepo(t)
	r.Page = 7
	var msgs []string
	for i := range 50 {
		msgs = append(msgs, fmt.Sprintf("event %d\n\n{\"n\":%d}\n", i, i))
	}
	shas := chain(t, r, "", msgs...)

	pages := readLog(t, r, "", shas[len(shas)-1], bus.MaxRecord)
	if len(pages) != 8 {
		t.Errorf("%d pages, want 8 of at most 7", len(pages))
	}
	var got []bus.Commit
	for _, p := range pages {
		if len(p) == 0 || len(p) > 7 {
			t.Errorf("a page of %d", len(p))
		}
		got = append(got, p...)
	}
	if len(got) != len(shas) {
		t.Fatalf("read %d commits, want %d", len(got), len(shas))
	}
	for i, c := range got {
		if c.SHA != shas[i] || c.Oversize || strings.TrimSpace(c.Message) != strings.TrimSpace(msgs[i]) || c.Time.IsZero() {
			t.Fatalf("commit %d: %+v, want %s with %q", i, c, shas[i], msgs[i])
		}
	}

	got = nil
	for _, p := range readLog(t, r, shas[40], shas[len(shas)-1], bus.MaxRecord) {
		got = append(got, p...)
	}
	if len(got) != 9 || got[0].SHA != shas[41] {
		t.Errorf("read from the middle: %d commits starting %s, want 9 from %s", len(got), got[0].SHA, shas[41])
	}
}

// A commit over the limit isn't read: it comes in its place, marked, with no message, so the reader
// passes over it and its cursor moves past it, and what follows it is read as usual. ReadCommit
// refuses it.
func TestLogPassesOverACommitOverTheLimit(t *testing.T) {
	r := logRepo(t)
	const max = 4 << 10
	huge := "huge\n\n{\"body\":\"" + strings.Repeat("x", 4*max) + "\"}\n"
	shas := chain(t, r, "", "before\n\n{}\n", huge, "after\n\n{}\n")

	var got []bus.Commit
	for _, p := range readLog(t, r, "", shas[2], max) {
		got = append(got, p...)
	}
	if len(got) != 3 {
		t.Fatalf("read %d commits, want 3", len(got))
	}
	if c := got[1]; c.SHA != shas[1] || !c.Oversize || c.Message != "" || c.Size <= max {
		t.Errorf("the large commit came as %+v", c)
	}
	if got[0].Oversize || got[2].Oversize || !strings.HasPrefix(got[2].Message, "after") {
		t.Errorf("the commits around it: %+v, %+v", got[0], got[2])
	}

	if _, err := r.ReadCommit(context.Background(), shas[1], max); !errors.Is(err, bus.ErrTooLarge) {
		t.Errorf("ReadCommit of the large commit: %v, want ErrTooLarge", err)
	}
	if c, err := r.ReadCommit(context.Background(), shas[1], 8*max); err != nil || !strings.HasPrefix(c.Message, "huge") {
		t.Errorf("ReadCommit within a higher limit: %v", err)
	}
	if c, err := r.ReadCommit(context.Background(), shas[2], max); err != nil || c.SHA != shas[2] || !strings.HasPrefix(c.Message, "after") {
		t.Errorf("ReadCommit of a small commit: %+v, %v", c, err)
	}
}

// An error from the page stops the read, and is what Log returns.
func TestLogStopsAtAPageError(t *testing.T) {
	r := logRepo(t)
	r.Page = 2
	shas := chain(t, r, "", "a\n\n{}\n", "b\n\n{}\n", "c\n\n{}\n", "d\n\n{}\n")
	stop := errors.New("stop")
	calls := 0
	err := r.Log(context.Background(), "", shas[3], bus.MaxRecord, func([]bus.Commit) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("Log returned %v after %d pages, want the page's error after 1", err, calls)
	}
}

// A log with a merge in it is read along first parents, so that wherever a read stops, the last
// commit it read stands for everything before it: a read resumed from there repeats nothing. A
// merge's other side, which Murmur never writes, isn't read.
func TestLogResumedPastAMergeRepeatsNothing(t *testing.T) {
	r := logRepo(t)
	ctx := context.Background()
	root := chain(t, r, "", "root\n\n{}\n")[0]
	a := chain(t, r, root, "a\n\n{}\n")[0]
	b := chain(t, r, root, "b\n\n{}\n")[0]
	tree, err := r.EmptyTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Git.Run(ctx, "merge\n\n{}\n", "-c", "user.name=test", "-c", "user.email=test@example.invalid",
		"commit-tree", "--no-gpg-sign", tree, "-p", a, "-p", b, "-F", "-")
	if err != nil {
		t.Fatal(err)
	}
	merge := strings.TrimSpace(out)
	tip := chain(t, r, merge, "after\n\n{}\n")[0]

	var all []string
	for _, p := range readLog(t, r, root, tip, bus.MaxRecord) {
		for _, c := range p {
			all = append(all, c.SHA)
		}
	}
	if want := []string{a, merge, tip}; strings.Join(all, " ") != strings.Join(want, " ") {
		t.Fatalf("read %v, want %v (first parents only)", all, want)
	}
	for i, cursor := range all {
		var rest []string
		for _, p := range readLog(t, r, cursor, tip, bus.MaxRecord) {
			for _, c := range p {
				rest = append(rest, c.SHA)
			}
		}
		if strings.Join(rest, " ") != strings.Join(all[i+1:], " ") {
			t.Errorf("resumed after %s: read %v, want %v", cursor, rest, all[i+1:])
		}
	}
}

// datedChain is chain with each commit written a minute after the one before, the first at start
// minutes, so that a test knows the order git hands two logs over in.
func datedChain(t *testing.T, r *bus.Repo, start int, msgs ...string) []string {
	t.Helper()
	var shas []string
	parent := ""
	for i, m := range msgs {
		at := &bus.Repo{Name: r.Name, Email: r.Email, Git: gitx.Git{Dir: r.Git.Dir,
			Env: []string{fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", 1_700_000_000+60*(start+i))}}}
		sha, err := at.NewCommit(context.Background(), parent, m)
		if err != nil {
			t.Fatal(err)
		}
		shas, parent = append(shas, sha), sha
	}
	return shas
}

// Recent reads back across logs, newest first by when each commit was written, says which log each
// is in, and stops at its limit or when its caller has enough.
func TestRecentReadsAcrossLogsNewestFirst(t *testing.T) {
	r := logRepo(t)
	r.Page = 2
	a := datedChain(t, r, 0, "a0\n\n{}\n", "a1\n\n{}\n", "a2\n\n{}\n") // minutes 0, 1, 2
	b := datedChain(t, r, 1, "b0\n\n{}\n", "b1\n\n{}\n", "b2\n\n{}\n") // minutes 1, 2, 3
	tipA, tipB := a[2], b[2]

	var missing []string // the tips the last read found nothing for
	recent := func(limit int, enough int, tips ...string) (got []string, pages int) {
		t.Helper()
		var err error
		missing, err = r.Recent(context.Background(), tips, limit, bus.MaxRecord, func(p []bus.Commit) bool {
			pages++
			for _, c := range p {
				subject, _, _ := strings.Cut(c.Message, "\n")
				switch c.Tip {
				case tipA:
					subject += "@a"
				case tipB:
					subject += "@b"
				default:
					subject += "@" + c.Tip
				}
				got = append(got, subject)
			}
			return len(got) < enough
		})
		if err != nil {
			t.Fatal(err)
		}
		return got, pages
	}

	// Every commit, each in its own log. The two written in one minute come in either order.
	got, _ := recent(100, 100, tipA, tipB)
	if len(got) != 6 || got[0] != "b2@b" || got[5] != "a0@a" {
		t.Fatalf("both logs whole: %v", got)
	}
	for _, log := range []string{"a", "b"} {
		var own []string
		for _, s := range got {
			if strings.HasSuffix(s, "@"+log) {
				own = append(own, s)
			}
		}
		if want := fmt.Sprintf("%[1]s2@%[1]s %[1]s1@%[1]s %[1]s0@%[1]s", log); strings.Join(own, " ") != want {
			t.Errorf("log %s came as %v, want %s", log, own, want)
		}
	}

	// The limit counts commits across the logs, not for each.
	if got, _ := recent(3, 100, tipA, tipB); len(got) != 3 || got[0] != "b2@b" || strings.HasPrefix(got[2], "a0") || strings.HasPrefix(got[2], "b0") {
		t.Errorf("the newest three: %v", got)
	}
	// A caller with enough is given no more pages.
	if got, pages := recent(100, 1, tipA, tipB); pages != 1 || len(got) != 2 {
		t.Errorf("stopped after the first page: %d pages, %v", pages, got)
	}
	// One log's tip, twice, is one log.
	if got, _ := recent(100, 100, tipA, tipA); strings.Join(got, " ") != "a2@a a1@a a0@a" {
		t.Errorf("one log named twice: %v", got)
	}
	if len(missing) != 0 {
		t.Errorf("tips that are here came back as missing: %v", missing)
	}
	// What isn't a commit here is passed over, and so is what git would read as an option. Only an
	// object that isn't here at all is missing: a fetch may bring it, and nothing will make a blob
	// a commit.
	blob, err := r.Git.Run(context.Background(), "junk", "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	gone := []string{strings.Repeat("0", 40), strings.Repeat("ab", 20)}
	junk := append([]string{strings.TrimSpace(blob), "--all", "refs/heads/main", ""}, gone...)
	if got, _ := recent(100, 100, append(junk, tipA)...); strings.Join(got, " ") != "a2@a a1@a a0@a" {
		t.Errorf("with junk among the tips: %v", got)
	}
	if strings.Join(missing, " ") != strings.Join(gone, " ") {
		t.Errorf("missing %v, want the two objects that aren't here, %v", missing, gone)
	}
	if got, pages := recent(100, 100, junk...); len(got) != 0 || pages != 0 || len(missing) != 2 {
		t.Errorf("junk alone: %v in %d pages, %d missing", got, pages, len(missing))
	}
}

// Recent hands over everything in one page, however many commits, where Log pages by LogPage: its
// caller waits on no cursor, and each page costs git commands.
func TestRecentReadsALongHistoryInOnePage(t *testing.T) {
	r := logRepo(t)
	ctx := context.Background()
	// Written by one git, as a commit at a time would take a minute: every commit on the branch
	// has the one before as its parent, and no files.
	const n = 2*bus.LogPage + 10
	var in strings.Builder
	for i := range n {
		msg := fmt.Sprintf("event %d\n\n{}\n", i)
		fmt.Fprintf(&in, "commit refs/heads/long\ncommitter test <test@example.invalid> %d +0000\ndata %d\n%s\n", 1_700_000_000+i, len(msg), msg)
	}
	if _, err := r.Git.Run(ctx, in.String(), "fast-import", "--quiet"); err != nil {
		t.Fatal(err)
	}
	out, err := r.Git.Run(ctx, "", "rev-parse", "refs/heads/long")
	if err != nil {
		t.Fatal(err)
	}
	tip := strings.TrimSpace(out)

	var sizes []int
	newest := ""
	if _, err := r.Recent(ctx, []string{tip}, n-5, bus.MaxRecord, func(p []bus.Commit) bool {
		sizes = append(sizes, len(p))
		if newest == "" && len(p) > 0 {
			newest = p[0].Message
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 || sizes[0] != n-5 || !strings.HasPrefix(newest, fmt.Sprintf("event %d\n", n-1)) {
		t.Errorf("pages of %v commits starting at %q, want one page of %d from the newest", sizes, newest, n-5)
	}
	// Log, over the same history, pages it.
	if pages := readLog(t, r, "", tip, bus.MaxRecord); len(pages) != 3 {
		t.Errorf("Log read it in %d pages, want 3", len(pages))
	}
}

// Recent marks a commit over the limit and leaves it unread, as Log does.
func TestRecentPassesOverACommitOverTheLimit(t *testing.T) {
	r := logRepo(t)
	const max = 4 << 10
	huge := "huge\n\n{\"body\":\"" + strings.Repeat("x", 4*max) + "\"}\n"
	shas := chain(t, r, "", "before\n\n{}\n", huge, "after\n\n{}\n")
	var got []bus.Commit
	_, err := r.Recent(context.Background(), []string{shas[2]}, 100, max, func(p []bus.Commit) bool {
		got = append(got, p...)
		return true
	})
	if err != nil || len(got) != 3 {
		t.Fatalf("read %d commits, %v; want 3", len(got), err)
	}
	for _, c := range got {
		if c.Tip != shas[2] {
			t.Errorf("commit %s is in the log of %q", c.SHA, c.Tip)
		}
		if large := c.SHA == shas[1]; c.Oversize != large || (c.Message == "") != large {
			t.Errorf("commit %s: oversize %v, message of %d bytes", c.SHA, c.Oversize, len(c.Message))
		}
	}
}
