package daemon

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/web"
)

// The page's messages from before this daemon started (#138). The daemon exits when idle, and
// the one `mur ui` starts next had read nothing, so the page opened on conversations with their
// beginnings gone. A daemon away for more than catchUpLimit also passes over what was sent
// meanwhile, as history nobody is waiting for, which left a hole in the middle of one.
//
// Both are still in the logs this clone has fetched, so they are read back from there: the newest
// commits across every device's log, up to where this daemon has read each, opened with the keys
// it holds. Nothing more is kept on disk for it, and no request goes to the remote. Keeping the
// page's messages in a file was the other way, and it would not have filled the hole: what a
// daemon passes over, it never reads.
//
// It is read in the background, so neither the daemon's start nor its first sync waits for it, and
// not before a page or a lookup asks: then, and after that whenever a sync has passed over a log's
// backlog, a backlog given up on has been fetched, or this device is given a team key, which opens
// what it couldn't read before.
//
// A session's start and stop are in the same commits, so the Recent view gets those back too. A
// status, a tag or a commit was never on the bus (activity.go), and is gone with the daemon.

// historyDepth is how many commits are read back for each message the page keeps, at most: a log
// holds starts, stops and tags events between its messages.
const historyDepth = 8

// A read of the logs is given historyLimit, so a git that hangs holds nothing up for good. One
// that fails is tried again after historyRetry, historyTries times in a row at most: what failed
// it may pass, and a clone whose git can't be run shouldn't have it run every minute for nothing.
const (
	historyLimit = 30 * time.Second
	historyRetry = time.Minute
	historyTries = 3
)

// kickHistory has the logs read back for the page, soon.
func (d *Daemon) kickHistory() {
	select {
	case d.historyKick <- struct{}{}:
	default:
	}
}

// historyLoop reads the logs back for the page each time it is asked to, from when a page or a
// lookup first wants them: most daemons are started by a session and never show the page, and
// those run no git for it.
func (d *Daemon) historyLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-d.historyWant:
	}
	first := d.historyRead
	retry := time.NewTimer(historyRetry)
	retry.Stop()
	defer retry.Stop()
	failed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.historyKick:
		case <-retry.C:
		}
		rctx, cancel := context.WithTimeout(ctx, historyLimit)
		err := d.readHistory(rctx)
		cancel()
		retry.Stop() // this read stands in for a retry still due
		if first != nil {
			close(first) // done or not: nothing waits on a read that failed
			first = nil
		}
		switch {
		case ctx.Err() != nil:
			return
		case err == nil:
			failed = 0
		default:
			d.log.Printf("reading the page's earlier messages back from the bus: %v", err)
			if failed++; failed < historyTries {
				retry.Reset(historyRetry)
			}
		}
	}
}

// historyWait is how long the page's first calls, and a lookup, wait for the logs to be read back.
const historyWait = 3 * time.Second

// wantHistory has the logs read back, if nothing has asked yet.
func (d *Daemon) wantHistory() {
	if d.historyWant != nil {
		d.historyAsked.Do(func() { close(d.historyWant) })
	}
}

// awaitHistory has the logs read back, if they haven't been, and waits for that first read, done
// or not: the page's first call, or a lookup, then has the earlier messages. The read is of what
// is on this disk, and takes about a second. Nothing is kept waiting past historyWait for one that
// takes longer, and once one call has waited that long no other waits at all: the page gets the
// messages at the call after the read is done.
func (d *Daemon) awaitHistory(ctx context.Context) {
	if d.historyRead == nil {
		return // a daemon Run didn't start
	}
	d.wantHistory()
	if d.historyLate.Load() {
		return
	}
	select {
	case <-d.historyRead:
		return
	default:
	}
	t := time.NewTimer(historyWait)
	defer t.Stop()
	select {
	case <-d.historyRead:
	case <-ctx.Done():
	case <-d.bgCtx.Done(): // the daemon is stopping
	case <-t.C:
		d.historyLate.Store(true)
	}
}

// readHistory reads the newest of what the logs hold, up to where this daemon has read each, and
// puts the messages and the sessions' starts and stops the page doesn't have among those it does.
// It stops at a log's cursor because what lies past it is still to be read by a sync, which also
// queues it and keeps a person's own.
func (d *Daemon) readHistory(ctx context.Context) error {
	d.mu.Lock()
	devOf, refOf := map[string]string{}, map[string]string{} // a log's tip → its device, and its ref
	var tips []string
	for ref, sha := range d.st.Processed {
		dev, ok := meshDevice(ref)
		if !ok || !strings.HasPrefix(ref, brand.MeshPrefix) || sha == "" || devOf[sha] != "" {
			continue
		}
		devOf[sha], refOf[sha] = dev, ref
		tips = append(tips, sha)
	}
	keys := d.keyringLocked()
	want := d.opt.PageMessages
	d.mu.Unlock()
	sort.Strings(tips)

	person := d.personAddr()
	var msgs []bus.Event
	var acts []web.Activity
	others := 0 // a person's own messages are counted apart, as keepMessageLocked does
	missing, err := d.repo.Recent(ctx, tips, historyDepth*want, d.maxRecord, func(commits []bus.Commit) bool {
		for _, c := range commits {
			if others >= want {
				break // newest first: the rest are older than the page keeps
			}
			e, ok := historyEvent(c, devOf[c.Tip], keys)
			if !ok {
				continue
			}
			switch e.Type {
			case bus.Message:
				msgs = append(msgs, e)
				if e.From != person && e.To != person {
					others++
				}
			case bus.SessionStarted, bus.SessionStopped:
				if a, ok := sessionAct(e); ok {
					acts = append(acts, a)
				}
			}
		}
		return others < want
	})
	if err != nil {
		return err
	}
	// Oldest first, so that two sent at the same moment stay in their log's order when sorted.
	slices.Reverse(msgs)
	slices.Reverse(acts)
	d.mu.Lock()
	defer d.mu.Unlock()
	// A cursor at a commit that isn't here is a backlog given up on before it was fetched, by this
	// daemon or the one before it. Its log was passed over whole just now, and is read back once
	// the fetch lands (advanceMesh), unless the cursor has moved since, which a sync does by reading.
	for _, sha := range missing {
		if ref := refOf[sha]; d.st.Processed[ref] == sha {
			if d.historyOwed == nil {
				d.historyOwed = map[string]bool{}
			}
			d.historyOwed[ref] = true
		}
	}
	d.mergeMessagesLocked(msgs)
	d.mergeActsLocked(acts)
	return nil
}

// historyEvent decodes commit c of dev's log as logEventWaitLocked does, but waits for nothing and
// notes nothing: a record this device can't open is left out, and a sync is what waits for its key.
func historyEvent(c bus.Commit, dev string, keys bus.Keyring) (bus.Event, bool) {
	var e bus.Event
	if c.Oversize || dev == "" || bus.Decode(c.Message, &e) != nil {
		return e, false
	}
	if e.Type == bus.Boxed {
		in, err := bus.DecryptEvent(e, dev, keys)
		if err != nil {
			return e, false
		}
		e = in
	}
	if e.Clean(); !e.Valid() {
		return e, false
	}
	from, _ := bus.SplitAddr(e.From)
	return e, from == dev
}

// mergeMessagesLocked puts the events the page doesn't have among its messages, all of them in the
// order they were sent, by their senders' clocks: what is read back has no order of arrival. Every
// message then gets a new number, so a page that asked before is given them all again, and passes
// over the ones it has. With nothing new to keep, nothing changes. Caller holds mu.
func (d *Daemon) mergeMessagesLocked(events []bus.Event) {
	all := make([]bus.Event, 0, len(d.msgs)+len(events))
	for _, m := range d.msgs {
		all = append(all, m.e)
	}
	seen := maps.Clone(d.msgIDs)
	if seen == nil {
		seen = map[string]bool{}
	}
	for _, e := range events {
		if !seen[e.ID] {
			seen[e.ID] = true
			all = append(all, e)
		}
	}
	if len(all) == len(d.msgs) {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	was, wasIDs, wasOwn, wasSeq := d.msgs, d.msgIDs, d.ownMsgs, d.msgSeq
	d.msgs, d.msgIDs, d.ownMsgs = nil, map[string]bool{}, 0
	for _, e := range all {
		d.keepMessageLocked(e)
	}
	// Everything read back was older than what the page keeps: as it was, numbers and all, so no
	// page is given again what it has.
	if len(d.msgs) == len(was) && !slices.ContainsFunc(d.msgs, func(m pageMsg) bool { return !wasIDs[m.e.ID] }) {
		d.msgs, d.msgIDs, d.ownMsgs, d.msgSeq = was, wasIDs, wasOwn, wasSeq
	}
}

// mergeActsLocked does for the Recent view's activity what mergeMessagesLocked does for messages.
// Caller holds mu.
func (d *Daemon) mergeActsLocked(acts []web.Activity) {
	all := make([]web.Activity, 0, len(d.acts)+len(acts))
	for _, p := range d.acts {
		all = append(all, p.a)
	}
	seen := maps.Clone(d.actIDs)
	if seen == nil {
		seen = map[string]bool{}
	}
	for _, a := range acts {
		if !seen[a.ID] {
			seen[a.ID] = true
			all = append(all, a)
		}
	}
	if len(all) == len(d.acts) {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	was, wasIDs, wasSeq := d.acts, d.actIDs, d.actSeq
	d.acts, d.actIDs = nil, map[string]bool{}
	for _, a := range all {
		d.recordActLocked(a)
	}
	if len(d.acts) == len(was) && !slices.ContainsFunc(d.acts, func(p pageAct) bool { return !wasIDs[p.a.ID] }) {
		d.acts, d.actIDs, d.actSeq = was, wasIDs, wasSeq
	}
}
