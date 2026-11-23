package mcpserver

import (
	"context"
	"errors"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/view"
	"io"
	"slices"
	"strings"
	"sync"
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

// mur lookup asks the daemon what the lookup tool does, as the thread's session, and prints how
// the tool's answer reads (#1119).
func TestALookupCommandAsksForTheSession(t *testing.T) {
	f := &fakeDaemon{}
	commandRepo(t, f)
	var out strings.Builder
	if err := Command(context.Background(), "lookup", []string{"quiet-otter", "--messages", "2"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	h := f.lastHello()
	got := f.requestsOf(proto.OpLookup)
	if len(got) != 1 || got[0].Session != h.ID || got[0].Name != "quiet-otter" || got[0].Limit != 2 {
		t.Errorf("requests %+v, want one lookup of quiet-otter, 2 messages, as %s", got, h.ID)
	}
	if out.String() != view.Lookup(proto.Response{}) {
		t.Errorf("printed %q", out.String())
	}
}

// mur tags changes the thread's session's tags as the tags tool does, and prints the tool's answer
// (#1120).
func TestATagsCommandChangesTheSessionsTags(t *testing.T) {
	f := &fakeDaemon{}
	commandRepo(t, f)
	var out strings.Builder
	if err := Command(context.Background(), "tags", []string{"--add", "reviewing,group:ui", "--remove", "old"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	h := f.lastHello()
	got := f.requestsOf(proto.OpTags)
	if len(got) != 1 || got[0].Session != h.ID || !slices.Equal(got[0].Add, []string{"reviewing", "group:ui"}) || !slices.Equal(got[0].Remove, []string{"old"}) {
		t.Errorf("requests %+v, want one tags request as %s", got, h.ID)
	}
	if out.String() != view.Tags(proto.Response{}) {
		t.Errorf("printed %q", out.String())
	}
}

// mur mute mutes, unmutes and lists as the thread's session, and prints the tool's answer (#1121).
func TestAMuteCommandMutesAsTheSession(t *testing.T) {
	f := &fakeDaemon{}
	commandRepo(t, f)
	var out strings.Builder
	if err := Command(context.Background(), "mute", []string{"quiet-otter"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Command(context.Background(), "mute", []string{"--unmute", "quiet-otter"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	h := f.lastHello()
	got := f.requestsOf(proto.OpMute)
	if len(got) != 2 || got[0].Session != h.ID || got[0].Name != "quiet-otter" || got[0].Unmute || !got[1].Unmute || got[1].Session != h.ID {
		t.Errorf("requests %+v, want a mute and an unmute of quiet-otter as %s", got, h.ID)
	}
	if out.String() != view.Muted(proto.Response{}) {
		t.Errorf("printed %q", out.String())
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
		{"lookup", []string{"quiet-otter"}, "", proto.Request{Op: proto.OpLookup, Name: "quiet-otter"}, false},
		{"lookup", []string{"quiet-otter", "--messages", "3"}, "", proto.Request{Op: proto.OpLookup, Name: "quiet-otter", Limit: 3}, false},
		{"lookup", []string{"--messages=3", "dev/sess"}, "", proto.Request{Op: proto.OpLookup, Name: "dev/sess", Limit: 3}, false},
		{"lookup", []string{"-messages", "3", "a"}, "", proto.Request{Op: proto.OpLookup, Name: "a", Limit: 3}, false},
		{"lookup", []string{"a", "b"}, "", proto.Request{}, true},
		{"lookup", []string{"a", "--messages"}, "", proto.Request{}, true},
		{"lookup", []string{"a", "--messages", "x"}, "", proto.Request{}, true},
		{"lookup", []string{"a", "--messages", "0"}, "", proto.Request{}, true},
		{"lookup", []string{"a", "--messages", "1", "--messages", "2"}, "", proto.Request{}, true},
		{"lookup", []string{"a", "--nope"}, "", proto.Request{}, true},
		{"tags", nil, "", proto.Request{Op: proto.OpTags}, false},
		{"tags", []string{"--add", "reviewing"}, "", proto.Request{Op: proto.OpTags, Add: []string{"reviewing"}}, false},
		{"tags", []string{"--add", "a,b", "--add=c", "--remove", "d"}, "", proto.Request{Op: proto.OpTags, Add: []string{"a", "b", "c"}, Remove: []string{"d"}}, false},
		{"tags", []string{"--add", "group:ui", "--for", "quiet-otter,brisk-finch"}, "", proto.Request{Op: proto.OpTags, Add: []string{"group:ui"}, For: []string{"quiet-otter", "brisk-finch"}}, false},
		{"tags", []string{"-add", `["a","b"]`}, "", proto.Request{Op: proto.OpTags, Add: []string{"a", "b"}}, false},
		{"tags", []string{"reviewing"}, "", proto.Request{}, true},
		{"tags", []string{"--add"}, "", proto.Request{}, true},
		{"tags", []string{"--nope", "x"}, "", proto.Request{}, true},
		{"mute", nil, "", proto.Request{Op: proto.OpMute}, false},
		{"mute", []string{"quiet-otter"}, "", proto.Request{Op: proto.OpMute, Name: "quiet-otter"}, false},
		{"mute", []string{"--unmute", "quiet-otter"}, "", proto.Request{Op: proto.OpMute, Name: "quiet-otter", Unmute: true}, false},
		{"mute", []string{"quiet-otter", "-unmute"}, "", proto.Request{Op: proto.OpMute, Name: "quiet-otter", Unmute: true}, false},
		{"mute", []string{"--unmute"}, "", proto.Request{}, true},
		{"mute", []string{"a", "b"}, "", proto.Request{}, true},
		{"mute", []string{"a", "--unmute=no"}, "", proto.Request{}, true},
		{"mute", []string{"a", "--nope"}, "", proto.Request{}, true},
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
		{"send", []string{"a", long, "the body"}, "", proto.Request{Op: proto.OpSend, To: "a", Subject: mustSubject(long), Body: strings.TrimSpace(long) + "\n\nthe body"}, false},
	} {
		got, err := commandRequest(c.name, c.args, strings.NewReader(c.stdin), io.Discard)
		if (err != nil) != c.bad {
			t.Errorf("%s %q: error %v", c.name, c.args, err)
			continue
		}
		if !c.bad && (got.Op != c.want.Op || got.Doing != c.want.Doing || got.To != c.want.To || got.Subject != c.want.Subject ||
			got.Body != c.want.Body || got.Unmute != c.want.Unmute || got.Name != c.want.Name || got.Limit != c.want.Limit || !slices.Equal(got.Add, c.want.Add) || !slices.Equal(got.Remove, c.want.Remove) || !slices.Equal(got.For, c.want.For) || strings.Join(got.Tags, ",") != strings.Join(c.want.Tags, ",")) {
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
		{"\u200b\nreal text", "", "real text", ""}, // the daemon drops the invisible line, so nothing is left for the body
		{"fits\n" + long, "", "fits", long},
		{"a  b", "", "a b", ""},
	} {
		s, b, err := subjectAndBody(c.subject, c.body)
		if err != nil || s != c.wantSubject || b != c.wantBody {
			t.Errorf("subjectAndBody(%q, %q) = %q, %q; want %q, %q", c.subject, c.body, s, b, c.wantSubject, c.wantBody)
		}
	}
	if s, b, _ := subjectAndBody(long, "\u200b"); b != strings.TrimSpace(long) || utf8.RuneCountInString(s) > 120 {
		t.Errorf("invisible body: %q, %q", s, b)
	}
	// A body past the daemon's cap whose text starts late is still a body: judged whole, not cut.
	late := strings.Repeat("\n", 40<<10) + "important"
	if _, b, _ := subjectAndBody(long, late); b != late {
		t.Errorf("a long body that starts with blank lines was replaced")
	}
}

// Sol's edge cases in splitting a send (#1037), each of which went wrong on main.
func TestSubjectAndBodySolsEdges(t *testing.T) {
	zw := "\u200b"
	// 1. A body over the daemon's cap is the agent's, blank or not: the daemon refuses it, as it did
	// before #1022, rather than the subject's second line going out in its place.
	spaces := strings.Repeat(" ", bus.MaxBody+1)
	if s, b, err := subjectAndBody("first\nsecond", spaces); err != nil || s != "first second" || b != spaces {
		t.Errorf("an oversized blank body: %q, %d bytes, %v", s, len(b), err)
	}
	// 2. Invisible characters take no room in the subject, and a text of nothing but them makes no
	// subject, which the daemon refuses as empty.
	if s, _, err := subjectAndBody(strings.Repeat(zw, 120)+"ready", ""); err != nil || s != "ready" {
		t.Errorf("a subject after 120 invisible characters: %q, %v", s, err)
	}
	if s, b, err := subjectAndBody(strings.Repeat(zw, 121), ""); err != nil || s != "" || b != "" {
		t.Errorf("121 invisible characters: %q, %q, %v", s, b, err)
	}
	// 3. A one-text send too long for a body is refused here, by what the agent gave.
	huge := strings.Repeat("x", bus.MaxBody+1024)
	if _, _, err := subjectAndBody(huge, ""); err == nil || !strings.Contains(err.Error(), "message is over 32 KB") || strings.Contains(err.Error(), "body") {
		t.Errorf("a 33 KB subject and no body: %v", err)
	}
	if _, err := commandRequest("send", []string{"a", huge}, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "message is over 32 KB") {
		t.Errorf("mur send TO with 33 KB of text: %v", err)
	}
	// 4. A body of malformed UTF-8 is a body: cleaned, its replacement character is longer than the
	// byte it stands for.
	if s, b, err := subjectAndBody("first\nsecond", "\xff\x78"); err != nil || s != "first second" || b != "\xff\x78" {
		t.Errorf("a malformed body: %q, %q, %v", s, b, err)
	}
	// 5. A first line that a turn would take for a cut subject (view.RepeatsSubject) keeps the whole
	// text in the body, so the line isn't hidden.
	text := "Wait…\nWaitlist rollout starts at noon"
	s, b, err := subjectAndBody(text, "")
	if err != nil || s != "Wait…" || b != text {
		t.Errorf("a first line ending in …: %q, %q, %v", s, b, err)
	}
	if view.RepeatsSubject(s, b) && !strings.Contains(b, "Wait…") {
		t.Errorf("the first line is hidden and not in the body")
	}
}

// mustSubject is the subject subjectFor makes of text.
func mustSubject(text string) string {
	s, _, _ := subjectFor(text)
	return s
}

// A send with a body keeps all of a subject too long for one line: what the subject can't hold
// goes at the top of the body, and a turn shows the body alone when the subject only repeats its
// start (#1049, seen on 2026-11-22 as "…rather than ope..." and the rest gone). A subject that
// fits, one whose lines joined fit, and a body the joined text would push over the daemon's cap,
// are as before.
func TestSendWithABodyKeepsALongSubject(t *testing.T) {
	long := "Re: PR for a theme setting: my user has me on Figma only right now, so I'm passing your request to them rather than opening one myself"
	s, b, err := subjectAndBody(long, "No PR from me.")
	if err != nil || utf8.RuneCountInString(s) > maxSubject || b != long+"\n\nNo PR from me." {
		t.Fatalf("a long subject with a body: %q, %q, %v", s, b, err)
	}
	if !view.RepeatsSubject(s, b) {
		t.Errorf("a turn would print the cut subject %q and then the body that repeats it", s)
	}

	if s, b, err := subjectAndBody("Title\nmore of the subject", "the body"); err != nil || s != "Title more of the subject" || b != "the body" {
		t.Errorf("a short subject of two lines with a body: %q, %q, %v", s, b, err)
	}
	twoLong := "Title\n" + long
	if s, b, err := subjectAndBody(twoLong, "the body"); err != nil || s != "Title" || b != long+"\n\nthe body" {
		t.Errorf("a long subject of two lines with a body: %q, %q, %v", s, b, err)
	}
	// Runs of spaces in the subject don't stop a turn from seeing the body repeats it.
	spaced := strings.Replace(long, ". ", ".  ", -1) + ".  And  more  words  here."
	if s, b, _ := subjectAndBody(spaced, "x"); !view.RepeatsSubject(s, strings.TrimSpace(b)) {
		t.Errorf("a subject with double spaces: %q is not seen as repeating %q", s, b)
	}
	if s, b, err := subjectAndBody("fits", "the body"); err != nil || s != "fits" || b != "the body" {
		t.Errorf("a subject that fits: %q, %q, %v", s, b, err)
	}
	full := strings.Repeat("b", bus.MaxBody-10)
	if s, b, err := subjectAndBody(long, full); err != nil || b != full || utf8.RuneCountInString(s) != maxSubject {
		t.Errorf("a body near the cap: %d runes, %d bytes, %v", utf8.RuneCountInString(s), len(b), err)
	}
}

// A one-text send's body is the text as written, not as the daemon cleans it: the daemon checks
// both for secrets, and a key a zero-width space splits from the next word is found only in the
// raw one (Opus review of bb531e85). And `mur send` judges a text of malformed bytes by all of it.
func TestOneTextBodyIsRaw(t *testing.T) {
	text := "hi\nAKIA" + "IOSFODNN7EXAMPLE\u200bnote"
	if s, b, err := subjectFor(text); err != nil || s != "hi" || b != "AKIA"+"IOSFODNN7EXAMPLE\u200bnote" {
		t.Errorf("subjectFor(%q) = %q, %q, %v", text, s, b, err)
	}
	if s, b, err := subjectFor("\u200b\r\nfirst\rsecond"); err != nil || s != "first" || b != "second" {
		t.Errorf("a CR line break and an invisible line: %q, %q, %v", s, b, err)
	}
	// A first line with a malformed byte is judged as the daemon stores it, so a turn that hides it
	// as a cut subject has it in the body.
	if s, b, err := subjectFor("w \xff...\nw \xff…"); err != nil || s != "w \uFFFD..." || b != "w \xff...\nw \xff…" {
		t.Errorf("a malformed first line ending in ...: %q, %q, %v", s, b, err)
	}
	if got, err := commandRequest("send", []string{"a", "\xff\x78"}, strings.NewReader(""), io.Discard); err != nil || got.Subject == "" {
		t.Errorf("mur send of malformed bytes: %+v, %v", got, err)
	}
}

// A send with a real body and a subject whose lines joined fit is as it was, and no visible text
// of a one-text send is lost.
func TestSubjectAndBodyKeepsEverything(t *testing.T) {
	if s, b, err := subjectAndBody("Wait…\nmore", "the body"); err != nil || s != "Wait… more" || b != "the body" {
		t.Errorf("a send with a body: %q, %q, %v", s, b, err)
	}
	for _, text := range []string{
		"one line",
		"first\nsecond\nthird",
		strings.Repeat("word ", 60) + "end",
		"Wait…\nWaitlist rollout starts at noon",
		"\u200b\u200b head\u200b line\nbody " + strings.Repeat("\u200b", 200) + "text",
		"Done...\nDone and dusted",
	} {
		s, b, err := subjectFor(text)
		if err != nil || utf8.RuneCountInString(s) > 120 {
			t.Errorf("subjectFor(%q): %q, %v", text, s, err)
			continue
		}
		clean := strings.Fields(bus.Text(text, len(text)))
		got := strings.Fields(bus.Text(b, len(b)*3))
		if !view.RepeatsSubject(s, strings.TrimSpace(bus.Text(b, len(b)*3))) {
			got = append(strings.Fields(strings.TrimSuffix(strings.TrimSuffix(s, "…"), "...")), got...)
		}
		if strings.Join(got, " ") != strings.Join(clean, " ") && !strings.HasSuffix(s, "…") {
			t.Errorf("subjectFor(%q) lost text: %q + %q", text, s, b)
		}
		if strings.HasSuffix(s, "…") && view.RepeatsSubject(s, strings.TrimSpace(bus.Text(b, len(b)*3))) && strings.Join(got, " ") != strings.Join(clean, " ") {
			t.Errorf("subjectFor(%q): a cut subject without the whole text in the body: %q", text, b)
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

// A send to several names, whose session Codex's server takes over at an ID of its own partway,
// still reaches them all: the send the daemon refuses as not registered goes again, once, as the
// session a new hello finds, and the ones before it don't (#953).
func TestACommandSendGoesOnAfterItsThreadIsTakenOver(t *testing.T) {
	const codex = "c0dec0"
	var mu sync.Mutex
	taken := false
	f := &fakeDaemon{
		helloReply: func(proto.Hello) (string, string) {
			mu.Lock()
			defer mu.Unlock()
			if taken {
				return "", codex // the thread's session is the server's now
			}
			return "", ""
		},
		sendReply: func(req proto.Request) proto.Response {
			mu.Lock()
			defer mu.Unlock()
			if req.To == "brave-heron" {
				taken = true // the server registered between the first send and this one
			}
			if taken && req.Session != codex {
				return proto.Response{Error: proto.NotRegistered, Refused: proto.RefusedSend}
			}
			return proto.Response{OK: true, Self: "dev/" + req.Session,
				Messages: []proto.MessageView{{ID: "m-" + req.To, To: "dev/" + req.To, ToName: req.To, Subject: req.Subject}}}
		},
	}
	commandRepo(t, f)
	var out strings.Builder
	if err := Command(context.Background(), "send", []string{"quiet-otter,brave-heron,calm-finch", "hello all"}, nil, &out, io.Discard); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var got []string
	for _, r := range f.sends() {
		got = append(got, r.To+" as "+r.Session)
	}
	first := threadID("thread-913")
	want := []string{"quiet-otter as " + first, "brave-heron as " + first, "brave-heron as " + codex, "calm-finch as " + codex}
	if !slices.Equal(got, want) {
		t.Errorf("sends %q, want %q", got, want)
	}
	if n := f.helloCount(); n != 2 {
		t.Errorf("%d hellos, want 2", n)
	}
	if strings.Contains(out.String(), "registered") {
		t.Errorf("the send reported a refusal:\n%s", out.String())
	}
}
