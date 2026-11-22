package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// commandRepo is a joined repo with a fake daemon, the command's working directory, and its
// thread's shell: T3CODE_THREAD_ID and T3's command line set, the thread in T3's database, and one
// agent above (#913).
func commandRepo(t *testing.T, f *fakeDaemon) {
	t.Helper()
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(p.Top)
	t.Setenv("T3CODE_THREAD_ID", "thread-913")
	t.Setenv("T3CODE_CLI_PATH", "/opt/t3/bin/t3")
	stubs(t, 1, nil)
}

// stubs puts the agents above the command and T3's database's answer for the thread in place.
func stubs(t *testing.T, agents int, threadErr error) {
	t.Helper()
	was, wasState := commandAgentsAbove, commandThreadState
	commandAgentsAbove = func() int { return agents }
	commandThreadState = func(context.Context, string, string, string) (t3db.State, error) { return t3db.State{}, threadErr }
	t.Cleanup(func() { commandAgentsAbove, commandThreadState = was, wasState })
}

// A command registers the thread's session at the thread's own ID, says it is a command's, and
// makes its request as that session; - reads the body from standard input.
func TestASendCommandRegistersTheThreadsSession(t *testing.T) {
	f := &fakeDaemon{}
	commandRepo(t, f)
	var out strings.Builder
	if err := Command(context.Background(), "send", []string{"quiet-otter", "the subject", "-"}, strings.NewReader("the body\n"), &out); err != nil {
		t.Fatal(err)
	}
	h := f.lastHello()
	if !h.Command || !h.ByThread || h.ID != threadID("thread-913") || h.T3Thread != "thread-913" || h.T3CLI != "/opt/t3/bin/t3" {
		t.Errorf("hello %+v, want a command's, by thread-913", h)
	}
	f.mu.Lock()
	reqs := append([]proto.Request(nil), f.requests...)
	f.mu.Unlock()
	if len(reqs) != 1 || reqs[0].Op != proto.OpSend || reqs[0].Session != h.ID || reqs[0].To != "quiet-otter" ||
		reqs[0].Subject != "the subject" || reqs[0].Body != "the body\n" {
		t.Errorf("requests %+v, want one send as %s", reqs, h.ID)
	}
	if out.Len() == 0 {
		t.Error("the send printed nothing")
	}
}

// Where nothing names the thread, or the variable is another agent's, or T3 has no such thread, the
// command says so and registers nothing.
func TestACommandWithNoThreadOfItsOwnStops(t *testing.T) {
	f := &fakeDaemon{}
	commandRepo(t, f)
	for _, c := range []struct {
		name   string
		set    func(*testing.T)
		want   string
		wantIs error
	}{
		{"no thread", func(t *testing.T) { t.Setenv("T3CODE_THREAD_ID", "") }, "", errNoThread},
		{"inherited", func(t *testing.T) { stubs(t, 2, nil) }, "", errInheritedThread},
		{"not in T3", func(t *testing.T) { stubs(t, 1, t3db.ErrNotFound) }, "T3's database has no thread", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.set(t)
			err := Command(context.Background(), "who", nil, nil, &strings.Builder{})
			switch {
			case err == nil:
				t.Fatal("ran")
			case c.wantIs != nil && !errors.Is(err, c.wantIs), c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Errorf("error %q", err)
			}
		})
	}
	if n := f.helloCount(); n != 0 {
		t.Errorf("%d hellos", n)
	}
}

func TestCommandRequests(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want proto.Request
		bad  bool
	}{
		{"who", []string{"model:opus*"}, proto.Request{Op: proto.OpWho, Tags: []string{"model:opus*"}}, false},
		{"status", []string{"moving", "auth"}, proto.Request{Op: proto.OpStatus, Doing: "moving auth"}, false},
		{"inbox", nil, proto.Request{Op: proto.OpInbox}, false},
		{"inbox", []string{"x"}, proto.Request{}, true},
		{"send", []string{"a"}, proto.Request{}, true},
		{"send", []string{"a,b", "s", "b"}, proto.Request{Op: proto.OpSend, To: "a,b", Subject: "s", Body: "b"}, false},
		{"lookup", nil, proto.Request{}, true},
	} {
		got, err := commandRequest(c.name, c.args, strings.NewReader(""))
		if (err != nil) != c.bad {
			t.Errorf("%s %q: error %v", c.name, c.args, err)
			continue
		}
		if !c.bad && (got.Op != c.want.Op || got.Doing != c.want.Doing || got.To != c.want.To || got.Subject != c.want.Subject ||
			got.Body != c.want.Body || strings.Join(got.Tags, ",") != strings.Join(c.want.Tags, ",")) {
			t.Errorf("%s %q: %+v, want %+v", c.name, c.args, got, c.want)
		}
	}
}

func TestAgentsInChain(t *testing.T) {
	for _, c := range []struct {
		chain []string
		want  int
	}{
		{[]string{"bash", "claude", "t3"}, 1},
		{[]string{"bash", "claude", "bash", "claude", "t3"}, 2},
		{[]string{"powershell", "localharness_external", "agy_acp_server", "t3"}, 1},
		{[]string{"powershell", "kiro-cli", "t3"}, 0},
		{[]string{"bash", "tmux", "bash", "claude"}, 0},
	} {
		got := agentsInChain(len(c.chain), func(i int) (string, string, bool) { return "/usr/bin/" + c.chain[i], "", true })
		if got != c.want {
			t.Errorf("%q: %d agents, want %d", c.chain, got, c.want)
		}
	}
}
