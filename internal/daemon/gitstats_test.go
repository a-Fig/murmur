package daemon_test

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/gitx"
)

// gitStats records every git the daemons in this test process run, so that a wait that gives up
// can say whether git was slow, and how many ran at once, in the minute before (#513). Every test
// shares the process, so the figures are the process's, not the test's.
var gitStats = &gitRecorder{}

func init() { gitx.Observe = gitStats.add }

type gitCall struct {
	sub        string
	start, end time.Time
}

type gitRecorder struct {
	mu    sync.Mutex
	calls []gitCall
	// dropped is how many of the earliest calls were let go to keep calls bounded.
	dropped int
}

// keepCalls bounds what is kept: a busy test process runs a few hundred git a second at most,
// and a daemon shard about 45,000 in all (macOS, 2026-11-13).
const keepCalls = 200000

func (r *gitRecorder) add(sub string, start time.Time, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, gitCall{sub, start, start.Add(took)})
	if over := len(r.calls) - keepCalls; over > keepCalls/10 {
		r.calls = append([]gitCall(nil), r.calls[over:]...)
		r.dropped += over
	}
}

// summary describes the git calls that ended within the last span: per command, how many, and
// their mean and longest time, and the most that ran at once.
func (r *gitRecorder) summary(span time.Duration) string {
	r.mu.Lock()
	since := time.Now().Add(-span)
	var recent []gitCall
	for _, c := range r.calls {
		if c.end.After(since) {
			recent = append(recent, c)
		}
	}
	dropped := r.dropped
	r.mu.Unlock()
	if len(recent) == 0 {
		return "no git ended in that time"
	}
	type agg struct {
		n          int
		total, max time.Duration
		all        []time.Duration
	}
	by := map[string]*agg{}
	type edge struct {
		at time.Time
		d  int
	}
	var edges []edge
	for _, c := range recent {
		a := by[c.sub]
		if a == nil {
			a = &agg{}
			by[c.sub] = a
		}
		took := c.end.Sub(c.start)
		a.n++
		a.total += took
		a.max = max(a.max, took)
		a.all = append(a.all, took)
		edges = append(edges, edge{c.start, 1}, edge{c.end, -1})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at.Before(edges[j].at) })
	peak, now := 0, 0
	for _, e := range edges {
		now += e.d
		peak = max(peak, now)
	}
	subs := make([]string, 0, len(by))
	for s := range by {
		subs = append(subs, s)
	}
	sort.Slice(subs, func(i, j int) bool { return by[subs[i]].total > by[subs[j]].total })
	var b strings.Builder
	fmt.Fprintf(&b, "%d git ended in the last %v, at most %d at once:", len(recent), span, peak)
	if dropped > 0 {
		fmt.Fprintf(&b, " (and %d earlier ones, no longer kept)", dropped)
	}
	for _, s := range subs {
		a := by[s]
		sort.Slice(a.all, func(i, j int) bool { return a.all[i] < a.all[j] })
		p95 := a.all[len(a.all)*95/100]
		fmt.Fprintf(&b, "\n  %-14s %6d  mean %-9v p95 %-9v longest %v", s, a.n, (a.total / time.Duration(a.n)).Round(time.Millisecond),
			p95.Round(time.Millisecond), a.max.Round(time.Millisecond))
	}
	return b.String()
}

// ran says whether any git was recorded.
func (r *gitRecorder) ran() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls) > 0 || r.dropped > 0
}
