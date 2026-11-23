package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/title"
)

// A conversation's title as the default status line (#66, docs/design/presence.md, "Who is on, and
// what they are doing"). Where an agent has set no status, the session's line in `who` is the title
// of the conversation it is in, read from the client's own state: T3's database, Codex's name for
// the thread, Claude Code's transcript. The title goes out in presence as `doing`, as a status
// does; the format has no field for it, and an older daemon shows it as it shows a status.

// titleLoop reads the titles every TitleEvery. Each pass reads each session's source once, so no
// database is read more often than that; a title takes at most that long to show after its source
// is known, or after the conversation renames.
func (d *Daemon) titleLoop(ctx context.Context) {
	t := time.NewTicker(d.opt.TitleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.refreshTitles(ctx)
	}
}

// titleJob is one session whose title is to be read, with what the read goes by.
type titleJob struct {
	id         string
	agent      string
	route      proto.Route
	transcript string
}

// refreshTitles reads the title of each session that has no status of its own, and presents any
// that changed. Nothing is read with mu held: the sources are files and databases other programs
// have open.
func (d *Daemon) refreshTitles(ctx context.Context) {
	d.mu.Lock()
	var jobs []titleJob
	for id, s := range d.sessions {
		if s.doing == "" && s.dropped.IsZero() {
			jobs = append(jobs, titleJob{id: id, agent: s.agent, route: s.route, transcript: s.transcript})
		}
	}
	d.mu.Unlock()

	changed := false
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		raw, known, err := readTitle(ctx, j)
		if !known {
			continue
		}
		var warn string
		d.mu.Lock()
		s := d.sessions[j.id]
		// A session that changed conversation while its title was being read has another source now.
		if s != nil && s.route == j.route && s.transcript == j.transcript {
			switch {
			case err != nil:
				// Keep the title it had: a lock, or a file mid-write, is not a rename.
				if !s.titleWarned {
					s.titleWarned = true
					warn = err.Error()
				}
			default:
				s.titleWarned = false
				if t := title.Clean(raw); t != s.title {
					s.title = t
					changed = true
				}
			}
		}
		d.mu.Unlock()
		if warn != "" {
			d.log.Printf("can't read the title of session %s: %s", bus.Addr(d.device, j.id), warn)
		}
	}
	if changed {
		d.kickPresence()
	}
}

// readTitle reads the title of the conversation a session is in. known is false when the session
// gives nothing to find it by: not in T3 with its thread found, no Codex thread ID, no transcript.
// Outside T3 the title of an agent other than Claude Code or Codex isn't read.
// A session in T3 is read from T3 only: its own client's name for the conversation is another
// title, and the two would take turns showing.
func readTitle(ctx context.Context, j titleJob) (raw string, known bool, err error) {
	r := j.route
	switch {
	case r.T3CLI != "":
		if r.T3Thread == "" {
			return "", false, nil
		}
		raw, err = t3db.TitleOf(ctx, r.T3CLI, r.T3Home, r.T3Thread)
		if errors.Is(err, t3db.ErrNotFound) {
			return "", true, nil // T3 has the thread's row not yet, or no more: no title
		}
		return raw, true, err
	case r.Native != "" && !ownTitleUnread(j.agent):
		// The session's own home, as its MCP server has it: CODEX_HOME, which join forwards to the
		// server when the session has one, else Codex's default (#88). The daemon's own CODEX_HOME
		// is the home of whichever session started it, so it is taken only for a route that names
		// no home, which comes from a server older than #88's, as such a server expects.
		raw, err = title.Codex(ctx, codexDir(r), r.Native)
		return raw, true, err
	case j.transcript != "":
		raw, err = title.Claude(j.transcript)
		return raw, true, err
	}
	return "", false, nil
}

// ownTitleUnread says whether agent is one that gives the daemon its own ID for the conversation
// but whose own name for it Murmur doesn't read: OpenCode, Muse, Pi, Grok, Antigravity and Cursor. Codex is the one whose
// ID is read, in Codex's state.
func ownTitleUnread(agent string) bool {
	for _, a := range []string{"opencode", "muse", "pi", "grok", "antigravity", "cursor"} {
		if agent == a || strings.HasPrefix(agent, a+" ") {
			return true
		}
	}
	return false
}

// titleSource is the part of a route that says which conversation's title to read, and where.
func titleSource(r proto.Route) [5]string {
	return [5]string{r.T3CLI, r.T3Home, r.T3Thread, r.Native, r.CodexHome}
}

// validTranscript says a hook's transcript path is one to read the title from: a path to a JSON
// lines file, which is all the hook has ever given.
func validTranscript(p string) bool {
	return p != "" && filepath.IsAbs(p) && strings.EqualFold(filepath.Ext(p), ".jsonl")
}
