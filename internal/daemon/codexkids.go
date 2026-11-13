package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/title"
)

// Sessions another session started, where T3's database doesn't say so (#452). They are
// delegates as a T3 delegate_task child is (#188, threads.go), and go off the mesh the same way:
// offMeshLocked and parentOfLocked ask kids first. What differs is how one is recognised.
//
// A hook says so (#455 and the like): a hook request with Child set marks the session whose
// client's ID is the request's Native, and ParentNative, if given, is the client's ID of the
// session that started it. The daemon keeps what was said, by Native, so that a session whose MCP
// server registers after the hook spoke is a child from its first refresh.
//
// Codex says so in its state (#454): no hook of Murmur's can ask Codex for a thread's parent, but
// Codex's database records a thread Codex started for another (title.CodexLineage): one a thread
// spawned, a review, a guardian. That needs the thread's ID, which a Codex session's MCP server
// learns only from a call to one of Murmur's tools (the route's Native), so a subagent that never
// calls one is on the mesh, with no way in but its inbox. A thread a Codex hook gave a session
// (codexThreadSessionLocked, #453) is read too, but never makes a child: a subagent's hook is
// never matched, so a subagent's thread there means the match was wrong, and the session gives
// the thread back instead. Pairing a hook by process alone (hookNative) gives no thread here: a
// subagent runs in its parent's process, so its hook can be paired with the session that spawned
// it.
//
// Only a Codex session outside T3 is looked at. Inside T3, whether a thread is a delegate is
// T3's to say (#188): Tyler's rule (2026-11-12, #452) is that a thread shown in the user's list
// of sessions is on the mesh, and T3 knows which of its threads those are.
//
// The parent, for the refusal's pointer and for crediting commits, is the session whose client's
// ID is the one the hook or Codex named. With none the child is off the mesh and its parent
// unnamed.
//
// Once a child, always: a session is never put back on the mesh, and stays a child while it is
// stopped with its inbox kept. Nothing is saved, so a daemon that restarts learns it again.
//
// Tools that run Codex as another agent's subagents (Claude Code's codex-sub plugin) get no
// handling of their own (Tyler, 2026-11-12, #481): to Codex their threads are a client's, like a
// terminal's, and so they are to Murmur.

// kid is a session another started: the client's ID of the session that started it, "" if
// nothing named one.
type kid struct{ parentNative string }

// codexThread is a Codex thread, by Codex's state directory and its ID.
type codexThread struct{ dir, thread string }

// maxKidNatives is how many hook-declared children are remembered by their client's ID.
const maxKidNatives = 2000

// codexDir is the Codex state directory a route's session uses: its own, as its MCP server gave
// it, else the daemon's CODEX_HOME, else Codex's default (#88; see readTitle).
func codexDir(r proto.Route) string {
	dir := r.CodexHome
	if !filepath.IsAbs(dir) {
		if dir = os.Getenv("CODEX_HOME"); !filepath.IsAbs(dir) {
			dir = title.DefaultCodexDir()
		}
	}
	return dir
}

// noteHookChildLocked keeps what a hook said of the session whose client's ID is native: that
// another session started it, and that one's client's ID, "" if the hook doesn't know. The
// sessions here with that ID are children at once. A parent once named isn't forgotten by a
// later request that names none. Caller holds mu.
func (d *Daemon) noteHookChildLocked(native, parentNative string) {
	if native == "" {
		return
	}
	if d.kidNative == nil {
		d.kidNative = map[string]string{}
	}
	if old, known := d.kidNative[native]; !known && len(d.kidNative) >= maxKidNatives {
		clear(d.kidNative) // a daemon that old relearns the live ones from their next hook
	} else if known && parentNative == "" {
		parentNative = old
	}
	d.kidNative[native] = parentNative
	marked := false
	for _, s := range d.nativeSessionsLocked(native) {
		marked = d.markKidLocked(s.id, parentNative) || marked
	}
	if marked {
		d.kickPresence()
		d.kickPush()
	}
}

// markKidLocked makes session id a child, and says whether it wasn't one. Caller holds mu.
func (d *Daemon) markKidLocked(id, parentNative string) bool {
	if d.kids == nil {
		d.kids = map[string]kid{}
	}
	k, was := d.kids[id]
	if parentNative != "" {
		k.parentNative = parentNative
	}
	d.kids[id] = k
	if !was {
		d.log.Printf("session %s was started by another session, so it is off the mesh", bus.Addr(d.device, id))
	}
	return !was
}

// refreshKids finds the sessions another session started: those a hook named, whose MCP server
// has since registered, and the Codex sessions outside T3 whose thread Codex's state says is a
// subagent's. It says whether any session became a child. A lineage read once stands, since
// Codex doesn't change who started a thread. Codex's database is read outside the lock.
func (d *Daemon) refreshKids(ctx context.Context) (changed bool) {
	type cand struct {
		id     string
		thread codexThread
	}
	d.mu.Lock()
	var cands []cand
	var need []codexThread
	for _, s := range d.sortedSessionsLocked() {
		if !s.dropped.IsZero() || !isAgent(s.agent, "codex") || s.route.T3CLI != "" || s.route.Native == "" {
			continue
		}
		c := cand{s.id, codexThread{codexDir(s.route), s.route.Native}}
		if _, known := d.codexLin[c.thread]; !known {
			need = append(need, c.thread)
		}
		cands = append(cands, c)
	}
	d.mu.Unlock()

	read := map[codexThread]title.Lineage{}
	failed := map[codexThread]error{}
	for _, k := range need {
		if ctx.Err() != nil {
			return false
		}
		l, err := d.opt.CodexLineage(ctx, k.dir, k.thread)
		switch {
		case err == nil:
			read[k] = l
		case !errors.Is(err, title.ErrNoThread): // Codex writes the row a moment after the thread starts, or never
			failed[k] = err
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.codexLin == nil {
		d.codexLin, d.codexWarned = map[codexThread]title.Lineage{}, map[codexThread]bool{}
	}
	for k, l := range read {
		d.codexLin[k] = l
		delete(d.codexWarned, k)
	}
	used := map[codexThread]bool{}
	for _, c := range cands {
		used[c.thread] = true
		if l, known := d.codexLin[c.thread]; known && l.Subagent {
			if s := d.sessions[c.id]; s != nil && s.nativeFromHook {
				// The thread came from a hook matched by time, and a subagent's thread is never
				// what a session's own server would have learned: the match was wrong (#453).
				d.dropHookThreadLocked(s, "Codex's state says that thread is a subagent's")
				changed = true
				continue
			}
			changed = d.markKidLocked(c.id, l.Parent) || changed
		}
		if err := failed[c.thread]; err != nil && !d.codexWarned[c.thread] {
			d.codexWarned[c.thread] = true
			d.log.Printf("can't read who started the Codex thread of session %s (%v); it stays on the mesh until that is read",
				bus.Addr(d.device, c.id), err)
		}
	}
	for k := range d.codexLin {
		if !used[k] {
			delete(d.codexLin, k)
		}
	}
	for k := range d.codexWarned {
		if !used[k] {
			delete(d.codexWarned, k)
		}
	}
	// A session a hook named before its MCP server registered.
	for _, s := range d.sessions {
		if parent, ok := d.kidNative[s.route.Native]; ok && s.route.Native != "" && s.claudeSession == "" {
			changed = d.markKidLocked(s.id, parent) || changed
		}
	}
	for id := range d.kids {
		if d.sessions[id] == nil && !d.receivesLocked(id) {
			delete(d.kids, id)
		}
	}
	return changed
}

// kidParentLocked is the session here that the child id answers to: the live one whose client's
// ID is the one named as its parent, "" if nothing named one or no such session is here. Caller
// holds mu.
func (d *Daemon) kidParentLocked(id string, k kid) string {
	if k.parentNative == "" {
		return ""
	}
	for _, s := range d.sortedSessionsLocked() {
		if s.id != id && s.dropped.IsZero() && s.route.Native == k.parentNative {
			return s.id
		}
	}
	return ""
}
