package title

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/a-Fig/murmur/internal/t3db"
)

// Lineage is who started a Codex thread, as Codex's state database records it (#454).
type Lineage struct {
	// Subagent says Codex started the thread for another thread: one a thread spawned, a review,
	// a guardian. threads.source is then a JSON object with a "subagent" key, where a thread a
	// person started has a plain word ("vscode", "exec").
	Subagent bool
	// Parent is the thread that spawned it (source's subagent.thread_spawn.parent_thread_id), ""
	// where Codex names none, as for a review.
	Parent string
}

// ErrNoThread says Codex's state has no row for the thread, which it writes a moment after the
// thread starts.
var ErrNoThread = errors.New("Codex's state doesn't have the thread")

// CodexLineage reads the Lineage of thread id from the newest state_N.sqlite in dir, read-only.
func CodexLineage(ctx context.Context, dir, id string) (Lineage, error) {
	if dir == "" || id == "" {
		return Lineage{}, errors.New("no Codex directory or thread to look up")
	}
	path := latestState(dir)
	if path == "" {
		return Lineage{}, errors.New("Codex's state database isn't there")
	}
	ctx, cancel := context.WithTimeout(ctx, Total)
	defer cancel()
	db, err := t3db.OpenReadOnly(path)
	if err != nil {
		return Lineage{}, err
	}
	defer db.Close()
	var source sql.NullString
	err = db.QueryRowContext(ctx, `SELECT source FROM threads WHERE id = ?`, id).Scan(&source)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Lineage{}, ErrNoThread
	case err != nil:
		return Lineage{}, fmt.Errorf("reading Codex's database: %w", err)
	}
	var l Lineage
	l.Subagent, l.Parent = subagentSource(source.String)
	return l, nil
}

// subagentSource reads threads.source: whether it is a subagent's, and the thread that spawned
// it. A subagent's is {"subagent": ...}, where the value is a non-empty word ("review") or an
// object ({"thread_spawn": {"parent_thread_id": ...}}, {"other": "guardian"}); anything else
// (a plain word, null, bool, number, array, or empty string) is not treated as a subagent.
func subagentSource(source string) (subagent bool, parent string) {
	var s struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal([]byte(source), &s) != nil {
		return false, ""
	}
	raw := bytes.TrimSpace(s.Subagent)
	if len(raw) == 0 {
		return false, ""
	}
	switch raw[0] {
	case '"':
		var kind string
		if json.Unmarshal(raw, &kind) != nil || kind == "" {
			return false, ""
		}
		return true, ""
	case '{':
		var how struct {
			Spawn struct {
				Parent string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		}
		_ = json.Unmarshal(raw, &how)
		return true, how.Spawn.Parent
	default:
		return false, ""
	}
}
