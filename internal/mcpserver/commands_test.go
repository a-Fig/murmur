package mcpserver

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

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
	if err := Command(context.Background(), "send", []string{"quiet-otter", "the subject", "-"}, strings.NewReader("the body\n"), &out, io.Discard); err != nil {
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
			err := Command(context.Background(), "who", nil, nil, &strings.Builder{}, io.Discard)
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
	long := strings.Repeat("word ", 60) + "end"
	for _, c := range []struct {
		name  string
		args  []string
		stdin string
		want  proto.Request
		bad   bool
	}{
		{"who", []string{"model:opus*"}, "", proto.Request{Op: proto.OpWho, Tags: []string{"model:opus*"}}, false},
		{"status", []string{"moving", "auth"}, "", proto.Request{Op: proto.OpStatus, Doing: "moving auth"}, false},
		{"inbox", nil, "", proto.Request{Op: proto.OpInbox}, false},
		{"inbox", []string{"x"}, "", proto.Request{}, true},
		{"lookup", nil, "", proto.Request{}, true},
		// The new form: one text, its subject made from it (#1027).
		{"send", []string{"a"}, "", proto.Request{}, true},
		{"send", []string{"a,b", "short"}, "", proto.Request{Op: proto.OpSend, To: "a,b", Subject: "short"}, false},
		{"send", []string{"a", "the subject\nthe body"}, "", proto.Request{Op: proto.OpSend, To: "a", Subject: "the subject", Body: "the body"}, false},
		{"send", []string{"a", "-"}, "from stdin\nand more\n", proto.Request{Op: proto.OpSend, To: "a", Subject: "from stdin", Body: "and more"}, false},
		{"send", []string{"a", "-"}, " \n", proto.Request{}, true},
		{"send", []string{"a", ""}, "", proto.Request{}, true},
		{"send", []string{"a", "​"}, "", proto.Request{}, true},
		{"send", []string{"a", "too", "many", "words"}, "", proto.Request{}, true},
		// --subject gives the subject, wherever it is, and the text is then the whole body.
		{"send", []string{"a", "--subject", "s", "one\ntwo"}, "", proto.Request{Op: proto.OpSend, To: "a", Subject: "s", Body: "one\ntwo"}, false},
		{"send", []string{"--subject=s", "a", "-"}, "piped\n", proto.Request{Op: proto.OpSend, To: "a", Subject: "s", Body: "piped\n"}, false},
		{"send", []string{"a", "-subject", "s"}, "", proto.Request{Op: proto.OpSend, To: "a", Subject: "s"}, false},
		{"send", []string{"a", "text", "--subject"}, "", proto.Request{}, true},
		{"send", []string{"a", "--subject=", "text"}, "", proto.Request{}, true},
		{"send", []string{"a", "--subject", " ", "text"}, "", proto.Request{}, true},
		{"send", []string{"a", "--subject", "-"}, "not read", proto.Request{Op: proto.OpSend, To: "a", Subject: "-"}, false},
		{"send", []string{"a", "--subject", "s", "b", "c"}, "", proto.Request{}, true},
		{"send", []string{"a", "--", "--subject"}, "", proto.Request{Op: proto.OpSend, To: "a", Subject: "--subject"}, false},
		// The old form, TO SUBJECT BODY, still means what it did.
		{"send", []string{"a,b", "s", "b"}, "", proto.Request{Op: proto.OpSend, To: "a,b", Subject: "s", Body: "b"}, false},
		{"send", []string{"a", "s", "-"}, "the body\n", proto.Request{Op: proto.OpSend, To: "a", Subject: "s", Body: "the body\n"}, false},
		{"send", []string{"a", long, "the body"}, "", proto.Request{Op: proto.OpSend, To: "a", Subject: oneLine(long), Body: "the body"}, false},
	} {
		got, err := commandRequest(c.name, c.args, strings.NewReader(c.stdin), io.Discard)
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

// The old form says, on stderr, what the new one is; the new form says nothing there.
func TestTheOldSendFormSaysTheNewOne(t *testing.T) {
	var note strings.Builder
	if _, err := commandRequest("send", []string{"a", "s", "b"}, strings.NewReader(""), &note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note.String(), "old form") || !strings.Contains(note.String(), "send TO TEXT") {
		t.Errorf("note %q", note.String())
	}
	note.Reset()
	if _, err := commandRequest("send", []string{"a", "one text"}, strings.NewReader(""), &note); err != nil || note.Len() != 0 {
		t.Errorf("the new form: %v, note %q", err, note.String())
	}
}

// A long text keeps all of itself in the body, where the subject's 120 characters used to cut it
// (#1021): the opening words are the subject.
func TestSendKeepsALongTextWhole(t *testing.T) {
	long := strings.Repeat("word ", 60) + "end"
	got, err := commandRequest("send", []string{"a", long}, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != strings.TrimSpace(long) {
		t.Errorf("body %q, want the whole text", got.Body)
	}
	if n := utf8.RuneCountInString(got.Subject); n > 120 || !strings.HasPrefix(got.Subject, "word word") || !strings.HasSuffix(got.Subject, "…") {
		t.Errorf("subject %q (%d runes)", got.Subject, n)
	}
	got, err = commandRequest("send", []string{"a", "--subject", long}, strings.NewReader(""), io.Discard)
	if err != nil || got.Body != strings.TrimSpace(long) || !strings.HasSuffix(got.Subject, "…") {
		t.Errorf("a long --subject and no text: %+v, %v", got, err)
	}
}

// subjectAndBody's edges (Opus review of dc334c5d): CRLF, a blank or invisible body, and a first
// line of only invisible characters, which keeps the whole text rather than a subject of nothing.
func TestSubjectAndBodyEdges(t *testing.T) {
	long := strings.Repeat("word ", 60) + "end"
	for _, c := range []struct{ subject, body, wantSubject, wantBody string }{
		{"first\r\nsecond", "", "first", "second"},
		{"short", "  ", "short", "  "},
		{"\u200b\nreal text", "", "real text", "\u200b\nreal text"},
		{"fits\n" + long, "", "fits", long},
		{"a  b", "", "a b", ""},
	} {
		s, b := subjectAndBody(c.subject, c.body)
		if s != c.wantSubject || b != c.wantBody {
			t.Errorf("subjectAndBody(%q, %q) = %q, %q; want %q, %q", c.subject, c.body, s, b, c.wantSubject, c.wantBody)
		}
	}
	if s, b := subjectAndBody(long, "\u200b"); b != strings.TrimSpace(long) || utf8.RuneCountInString(s) > 120 {
		t.Errorf("invisible body: %q, %q", s, b)
	}
	// A body past the daemon's cap whose text starts late is still a body: judged whole, not cut.
	late := strings.Repeat("\n", 40<<10) + "important"
	if _, b := subjectAndBody(long, late); b != late {
		t.Errorf("a long body that starts with blank lines was replaced")
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
