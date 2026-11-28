package daemon

import (
	"context"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// OpenCode, Muse, Grok and the rest give their own session IDs too, which are no Codex thread's:
// outside T3 no title is looked for by them, in Codex's state or anywhere. Pi's is read from its
// session file (title.Pi).
func TestNoCodexTitleForTheOtherAgents(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"opencode", "muse", "grok", "antigravity", "cursor", "kiro", "kiro in T3"} {
		if _, known, err := readTitle(context.Background(), titleJob{agent: agent, route: proto.Route{Native: "ses_1"}}); known || err != nil {
			t.Errorf("%s: known %v, err %v", agent, known, err)
		}
	}
	for _, agent := range []string{"codex", "pi", "pi in T3", "piper", ""} {
		if ownTitleUnread(agent) {
			t.Errorf("%q counts as an agent whose title isn't read", agent)
		}
	}
}
