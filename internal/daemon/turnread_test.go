package daemon_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// A message handed to a T3 thread as a turn is read once the turn starts (#611): the hook's
// UserPromptSubmit carries the prompt, and a prompt that holds the message marks it. One the user
// typed meanwhile doesn't.
func TestATurnsMessageIsReadWhenItsPromptComes(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })

	id := pa.sendText(bobName, "Can you rerun CI?\nIt flaked on Windows.")
	eventually(t, "the handoff", func() bool { return len(calls()) > 0 })
	eventually(t, "handed over", pa.stateIs(id, bob, bus.MarkHanded))
	turn := calls()[0].Stdin

	// The user's own prompt, typed while T3 held Murmur's turn: still handed over.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true, Prompt: "fix the lint errors"})
	b.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-bob"})
	if r, _ := pa.receipt(id, bob); r.State != bus.MarkHanded {
		t.Fatalf("after the user's own prompt the receipt is %+v, want still handed over", r)
	}

	// Murmur's turn starts: its prompt is what was handed over.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true, Prompt: turn})
	eventually(t, "read: the turn holding it started", pa.stateIs(id, bob, bus.MarkRead))
}

// A turn's prompt marks its own message, not an older one whose subject starts the same (#651): a
// turn for "Deploy" was cancelled, and the prompt of the turn for "Deploy now" has "Deploy" at the
// start of a line.
func TestATurnMarksOnlyItsOwnMessage(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })

	cancelled := pa.sendText(bobName, "Deploy")
	eventually(t, "the first handoff", func() bool { return len(calls()) == 1 })
	eventually(t, "the first handed over", pa.stateIs(cancelled, bob, bus.MarkHanded))
	b.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-bob"}) // the turn never started

	id := pa.sendText(bobName, "Deploy now")
	eventually(t, "the second handoff", func() bool { return len(calls()) == 2 })
	eventually(t, "the second handed over", pa.stateIs(id, bob, bus.MarkHanded))

	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true, Prompt: calls()[1].Stdin})
	eventually(t, "read: the turn for it started", pa.stateIs(id, bob, bus.MarkRead))
	if r, _ := pa.receipt(cancelled, bob); r.State != bus.MarkHanded {
		t.Fatalf("the cancelled turn's message is %+v, want still handed over", r)
	}
}

// A prompt is matched to the turn view.Turn rendered, as the daemon handed it over: after a line a
// client put before it, cut at the hook's 4 KB, or with its line ends changed. A prompt the user
// typed, or another message's turn, isn't it, nor is a cut prompt that two turns could be.
func TestAPromptIsMatchedToItsTurn(t *testing.T) {
	msg := func(subject, body string) string {
		return view.Turn(proto.MessageView{From: "d/aaaaaa", FromName: "quiet-otter", To: "d/bbbbbb", Subject: subject, Body: body})
	}
	// hook cuts a prompt as the hook does: at proto.MaxPrompt bytes, where a character starts.
	hook := func(p string) string {
		if len(p) <= proto.MaxPrompt {
			return p
		}
		n := proto.MaxPrompt
		for !utf8.RuneStart(p[n]) {
			n--
		}
		return p[:n]
	}
	rerun := msg("Rerun CI?", "It flaked.")
	cut := msg("A long first line that the page cut…", "A long first line that the page cut short, and went on.")
	long := msg("Logs", strings.Repeat("é line of the log\n", 400))
	longAgain := msg("Logs", strings.Repeat("é line of the log\n", 400)+"and one more")
	deploy, deployNow := msg("Deploy", ""), msg("Deploy now", "")
	// A line a client put first leaves room in the hook's 4 KB for the turn's header and "Deploy".
	before := strings.Repeat("a", proto.MaxPrompt-len("Message from quiet-otter via Murmur:\n\nDeploy")-1) + "\n"
	for _, c := range []struct {
		name   string
		prompt string
		turns  []string
		want   int
	}{
		{"the turn itself", rerun, []string{rerun}, 0},
		{"a subject that starts the body", cut, []string{cut}, 0},
		{"after a line a client put first", "[from T3]\n" + rerun, []string{rerun}, 0},
		{"with Windows line ends, trimmed", strings.TrimSpace(strings.ReplaceAll(rerun, "\n", "\r\n")), []string{rerun}, 0},
		{"cut at the hook's 4 KB", hook(long), []string{long}, 0},
		{"two long turns that share their first 4 KB", hook(longAgain), []string{long, longAgain}, -1},
		{"cut, after lines a client put first", hook("[from T3]\n" + long), []string{rerun, long}, 1},
		{"the later of two whose subjects start the same", deployNow, []string{deploy, deployNow}, 1},
		{"the earlier of them", deploy, []string{deploy, deployNow}, 0},
		{"the oldest of two alike", rerun, []string{rerun, rerun}, 0},
		{"the same subject, another body", msg("Rerun CI?", "It flaked again."), []string{rerun}, -1},
		{"the user's own words", "run the tests", []string{msg("test", "")}, -1},
		{"the user's words under a line like a header", "Message me later\ntest", []string{msg("test", "")}, -1},
		{"the header alone", strings.SplitN(rerun, "\n", 2)[0], []string{rerun}, -1},
		{"a turn's start, not cut", strings.SplitN(deployNow, "\n\n", 3)[0] + "\n\nDeploy", []string{deploy, deployNow}, -1},
		{"cut where two turns still agree", hook(before + deployNow), []string{deploy, deployNow}, -1},
		{"cut, where only one turn starts so", hook(before + deployNow), []string{rerun, deployNow}, 1},
	} {
		if got := daemon.WhichTurn(c.prompt, c.turns...); got != c.want {
			t.Errorf("%s: the prompt is taken for turn %d, want %d:\n%s", c.name, got, c.want, c.prompt)
		}
	}
}

// A daemon that restarts knows a session only once its MCP server says hello again, and a turn
// handed over before the restart can start in that time. Its prompt still marks its message read
// once the session is back (#658): it was dropped, and the message stayed handed over, its
// sender's tell_me waiting out its 30 minutes. Claude Code's hook names the session by its ID;
// Codex's by its thread's, which its server knows as its route's Native.
func TestATurnThatStartsWhileItsDaemonRestartsIsRead(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, claude, agent string
		native              string
	}{
		{name: "claude", claude: "claude-bob"},
		{name: "codex", agent: "codex", native: "codex-thread"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			remote := newRemote(t)
			exe, calls := useFakeT3(t)
			a, b := newDevice(t, remote), newDevice(t, remote)
			route := proto.Route{T3CLI: exe, T3Thread: "thread-bob", Native: c.native}
			bob, _ := b.sessionRoute("bbbbbb", c.claude, route)
			bobName := b.named(bob)
			pa := a.page()
			pa.choose("ada")
			eventually(t, "presence", func() bool { return a.seesSession(bob) })

			id := pa.sendText(bobName, "Can you rerun CI?")
			eventually(t, "the handoff", func() bool { return len(calls()) > 0 })
			eventually(t, "handed over", pa.stateIs(id, bob, bus.MarkHanded))
			turn := calls()[0].Stdin

			b.restart()
			hook := proto.Request{Op: proto.OpNotices, ClaudeSession: c.claude, Whole: true, Prompt: turn}
			if c.agent != "" {
				hook.Agent, hook.ClaudeSession = c.agent, c.native
			}
			b.do(hook)
			if !pa.stateIs(id, bob, bus.MarkHanded)() {
				t.Fatal("marked before bob was back")
			}
			conn, _, err := b.c.Hello(context.Background(), proto.Hello{ID: "bbbbbb", Agent: "test",
				ClaudeSession: c.claude, Route: route, Reconnect: true})
			if err != nil {
				t.Fatalf("bob's hello after the restart: %v", err)
			}
			t.Cleanup(func() { conn.Close() })
			eventually(t, "read: the turn started while bob reconnected", pa.stateIs(id, bob, bus.MarkRead))
		})
	}
}
