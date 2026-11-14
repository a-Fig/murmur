package view

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// fromPage is a message a person wrote on the page with no subject, as it reaches a session: split
// by web.SplitText, its subject made one line by the send path.
func fromPage(text string) proto.MessageView {
	subject, body := web.SplitText(text)
	return proto.MessageView{
		From: "dev-0001/" + bus.PersonSession, FromName: "ada", FromAgent: bus.AgentPerson,
		To: "x", Explained: true, Subject: bus.OneLine(subject, 120), Body: body,
	}
}

// A first line too long for a subject is cut short for one, and the body has the whole text, so
// the turn and the inbox print the body alone and the agent reads that line once (#552).
func TestAPageMessageIsReadOnce(t *testing.T) {
	long := "Could you look at why the release build fails on Windows when the cache is cold and the runner has just restarted, please"
	cases := map[string]string{
		"one line":              long,
		"more lines":            long + "\n\nThe log is in the PR.",
		"surrounding spaces":    "  \n" + long + "  \nand more\n\n",
		"CRLF":                  long + "\r\nsecond line",
		"multi-byte at the cut": strings.Repeat("é", 60) + strings.Repeat("日本", 40),
		"emoji at the cut":      strings.Repeat("🚀", 130),
		"no space to cut at":    strings.Repeat("x", 200),
	}
	for name, text := range cases {
		m := fromPage(text)
		if !strings.HasSuffix(m.Subject, "…") {
			t.Fatalf("%s: the subject %q isn't cut short; the case tests nothing", name, m.Subject)
		}
		stem := strings.TrimSuffix(m.Subject, "…")
		body := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
		want := "Message from ada [human] via " + brand.Product + ":\n\n" + body + "\n\nReply with " + brand.Product + "'s `send` to ada if a reply is needed.\n"
		if got := Turn(m); got != want {
			t.Errorf("%s: the turn:\n%s\nwant:\n%s", name, got, want)
		}
		got := Inbox(proto.Response{Messages: []proto.MessageView{m}})
		if strings.Contains(got, "Subject:") || !strings.Contains(got, "\n> "+stem) {
			t.Errorf("%s: the inbox:\n%s", name, got)
		}
		if n := strings.Count(got, stem); n != 1 {
			t.Errorf("%s: the inbox has the first line %d times:\n%s", name, n, got)
		}
	}
}

// A subject that is a subject stays, with the body after it: one that fits whole, one that ends in
// "…" but isn't the body's start, and the cases at the edges.
func TestARealSubjectIsKept(t *testing.T) {
	cases := []struct{ name, subject, body string }{
		{"a short first line", "Release build fails", "The log is in the PR."},
		{"an ellipsis of its own", "Wait for it…", "The build is green."},
		{"three dots of its own", "Thinking...", "Let's ship it."},
		{"the body's start without a cut", "Release", "Release build fails"},
		{"no body", "Release build fails…", ""},
		{"a body that differs after the cut", "Release build fails on Win…", "Release build fails on macOS"},
	}
	for _, c := range cases {
		m := proto.MessageView{From: "dev-0001/abc123", FromName: "quiet-otter", FromAgent: "Codex", To: "x", Introduced: true, Subject: c.subject, Body: c.body}
		want := "Message from quiet-otter via " + brand.Product + ":\n\n" + c.subject + "\n"
		if c.body != "" {
			want += "\n" + c.body + "\n"
		}
		want += "\nReply with " + brand.Product + "'s `send` to quiet-otter if a reply is needed.\n"
		if got := Turn(m); got != want {
			t.Errorf("%s: the turn:\n%s\nwant:\n%s", c.name, got, want)
		}
		if got := Inbox(proto.Response{Messages: []proto.MessageView{m}}); !strings.Contains(got, "Subject: "+c.subject+"\n") {
			t.Errorf("%s: the inbox:\n%s", c.name, got)
		}
	}
}

// An agent's subject ended with "..." over a body that goes on from it is the same cut, so it goes
// too, as the page leaves it out.
func TestAThreeDotCutIsLeftOut(t *testing.T) {
	m := proto.MessageView{From: "dev-0001/abc123", FromName: "quiet-otter", To: "x", Introduced: true, Subject: "Could you look at...", Body: "Could you look at the release build?"}
	if got := Turn(m); strings.Contains(got, "Could you look at...") || !strings.Contains(got, ":\n\nCould you look at the release build?\n") {
		t.Errorf("the turn:\n%s", got)
	}
	if got := Inbox(proto.Response{Messages: []proto.MessageView{m}}); strings.Contains(got, "Subject:") {
		t.Errorf("the inbox:\n%s", got)
	}
}

// The hook gives a message whole as its turn reads, so it reads the first line once too; its
// notices print the subject alone, never the body, so there is nothing there to repeat.
func TestTheHookReadsAPageMessageOnce(t *testing.T) {
	m := fromPage(strings.Repeat("word ", 40) + "end")
	stem := strings.TrimSuffix(m.Subject, "…")
	if got := Hook([]proto.MessageView{m}, nil, 0); strings.Count(got, stem) != 1 {
		t.Errorf("the hook's whole message:\n%s", got)
	}
	if got := Notices([]proto.MessageView{m}, 1); strings.Count(got, stem) != 1 || !strings.Contains(got, m.Subject) {
		t.Errorf("the notice:\n%s", got)
	}
}
