// This file is what the tool handlers share: a call made as the session (callOn, which asks again
// whether the server is on), and what a send or a status carries, shared with the shell commands
// in commands.go.

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/scrub"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// call sends a request as this session, waiting briefly for the registration if it is still
// being made (the first call can arrive while the daemon is starting).
func (s *server) call(ctx context.Context, req proto.Request) (proto.Response, error) {
	s.mu.Lock()
	c, l := s.c, s.link
	s.mu.Unlock()
	return s.callOn(ctx, c, l, req)
}

// callFrom is call for a tool call, which says something about its session first: Codex's thread
// in its _meta, or the OpenCode session a shared server's call comes from.
func (s *server) callFrom(ctx context.Context, mreq *mcp.CallToolRequest, req proto.Request) (proto.Response, error) {
	if s.direct != nil {
		return s.direct(req)
	}
	if !sharedAgent(s.opt.Agent) {
		s.noteMeta(ctx, mreq)
		return s.call(ctx, req)
	}
	l := s.sessionLinkFor(mreq)
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	return s.callOn(ctx, c, l, req)
}

// callOn is call by link l of client c.
func (s *server) callOn(ctx context.Context, c *client.Client, l *link, req proto.Request) (proto.Response, error) {
	if l == nil {
		return proto.Response{}, errPassive
	}
	id, err := l.wait(ctx, 15*time.Second)
	if err != nil {
		return proto.Response{}, err
	}
	// After the registration, whose reply may say the daemon couldn't read the thread (#980).
	if mutates(req.Op) {
		if err := l.classifyFirst(ctx); err != nil {
			return proto.Response{}, err
		}
	}
	// The poll that turns the server off runs every few seconds; a call in between must not reach a
	// daemon that is still up, or publish for a user who has left.
	if l.allowed != nil && !l.allowed() { // the repo the link was made in, which a move doesn't change
		return proto.Response{}, errPassive
	}
	// A move or a leave ends the link's life, and with it a call still on its way to that repo's
	// daemon: the server is no longer there.
	if life := l.ctx; life != nil {
		if life.Err() != nil {
			return proto.Response{}, errPassive
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(life, cancel)()
	}
	req.Session = id
	resp, err := c.Do(ctx, req)
	if err != nil {
		return resp, fmt.Errorf("can't reach the %s daemon: %w", brand.Product, err)
	}
	l.learn(resp.ClaudeSession)
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// noteMeta takes the thread ID Codex puts in the _meta of every tool call (`threadId`): Codex's MCP
// servers get a bare environment, so a call is the first they hear of it. The session is a T3 thread
// if T3's database has that ID.
func (s *server) noteMeta(ctx context.Context, req *mcp.CallToolRequest) {
	s.mu.Lock()
	l := s.link
	s.mu.Unlock()
	if l == nil {
		return
	}
	l.callCame()
	if req == nil || req.Params == nil {
		return
	}
	if c := codexCallOf(req.Params.GetMeta()); c.thread != "" {
		// What learnNative starts outlives the call, but not the link: it can reach the daemon, and
		// even start one, so it ends with the link and Serve waits for it.
		if l.ctx != nil {
			ctx = l.ctx
		} else {
			ctx = context.WithoutCancel(ctx)
		}
		l.learnNative(ctx, c)
	}
}

// maxRecipients is the most recipients one send can name. Each is a push of its own, one after
// another, and a client that stops waiting for the tool call partway leaves the sender with no
// result and some recipients with the message, so a send that tried again would repeat it. Ten
// pushes fit inside the minute Codex waits for a tool call (2026-11-13).
const maxRecipients = 10

// recipients splits send's to into the recipients it names (#520): one, or several separated by
// commas, or a JSON array of them written into the string, which is how a model may pass a list
// to a field that takes a string. Blanks are dropped, and so is a recipient named again.
func recipients(to string) []string {
	var list []string
	if t := strings.TrimSpace(to); !strings.HasPrefix(t, "[") || json.Unmarshal([]byte(t), &list) != nil {
		list = strings.Split(t, ",")
	}
	var out []string
	for _, r := range list {
		if r = strings.TrimSpace(r); r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// sendEach sends one message to each of several recipients, as a send to each would (#520): every
// one gets an ordinary message of its own, and none is told who else got it. So the daemon needs
// nothing new, and an older one takes a list too. A group or everyone can't be in a list: a message
// there is one conversation that all of its members see, which a list is not.
//
// Names go first, and addresses after, so that an address of a session already sent to by its name
// is known for one and skipped: the daemon says which address a name reached. Two names for the
// same session (one it had before a move, #36) would each send, as would a device and one of its
// sessions. A refusal that isn't the recipient's alone (refusedRecipient) is about the message or
// this session (no subject, a key in the body, not registered, no daemon) and would be the same for
// the rest, so the rest aren't tried: a session waiting to register would otherwise wait once per
// recipient.
func (s *server) sendEach(ctx context.Context, mreq *mcp.CallToolRequest, to []string, subject, body, tell string, files []string) (*mcp.CallToolResult, any, error) {
	out, sent := sendEachVia(func(req proto.Request) (proto.Response, error) { return s.callFrom(ctx, mreq, req) },
		to, subject, body, tell, files)
	res := text(out)
	res.IsError = !sent
	return res, nil, nil
}

// sendEachVia is sendEach, making each send with call: the tool's, or a shell command's (#913). It
// returns what to show, and whether anything was sent.
func sendEachVia(call func(proto.Request) (proto.Response, error), to []string, subject, body, tell string, files []string) (string, bool) {
	if len(to) > maxRecipients {
		return fmt.Sprintf("Not sent: a send can name at most %d recipients, and this one names %d.", maxRecipients, len(to)), false
	}
	order := make([]int, 0, len(to))
	for pass := range 2 {
		for i, r := range to {
			if strings.Contains(r, "/") == (pass == 1) {
				order = append(order, i)
			}
		}
	}
	rs := make([]view.SentTo, len(to))
	reached := map[string]string{} // address → the recipient as named that reached it
	sent := false
	stopped := ""
	for _, i := range order {
		r := to[i]
		rs[i].To = r
		if stopped != "" {
			rs[i].Err = "not tried, as " + stopped
			continue
		}
		// A device's group (tags.DevicePrefix) can pass the length bus.IsGroup allows.
		if r == "*" || bus.IsGroup(tags.Normalize(r)) || strings.HasPrefix(tags.Normalize(r), tags.DevicePrefix) {
			rs[i].Err = "a list is for sessions and people, each of whom gets a message of their own; send to " + r + " on its own"
			continue
		}
		if first, ok := reached[r]; ok {
			rs[i].Same = first
			continue
		}
		resp, err := call(proto.Request{Op: proto.OpSend, To: r, Subject: subject, Body: body, TellMe: tell, Files: files})
		resp = untold(resp, err, tell)
		if err != nil {
			rs[i].Err = err.Error()
			if !refusedRecipient(resp, rs[i].Err, r, subject) {
				stopped = "the send to " + r + " failed for a reason that isn't about " + r
			}
			continue
		}
		rs[i].Resp, sent = resp, true
		if len(resp.Messages) > 0 {
			reached[resp.Messages[0].To] = r
		}
	}
	return view.SentEach(rs), sent
}

// untold adds to a sent message's note that its sender won't be told how far it got, though it
// asked (#276): the daemon is older than tell_me, and passed over it.
func untold(resp proto.Response, err error, tell string) proto.Response {
	if err == nil && tell != "" && !resp.TellKnown && resp.Error == "" {
		resp.Note = strings.TrimSpace(resp.Note + " You won't be told how far it gets: this device's " + brand.Product +
			" is too old for tell_me, and updates itself within the hour.")
	}
	return resp
}

// refusedRecipient says whether the refusal why, of the send of subject to r, is r's alone, so
// that the rest of a list can still be sent to. The daemon says so (resp.Refused, #541); one from
// before that is judged by its words. Its refusals for a recipient name it as it was given, but
// for a recipient that isn't a name or an address at all (its "a message needs a recipient ...
// and a subject", which with a subject given is about the recipient), a mute or an offline
// session at an address whose messages go on to another (forwardLocked), which it names by that
// one, and a recipient whose device has no key to encrypt to, which it names by its device. A call
// no daemon answered (no daemon, or a session not yet registered) is about the sender, and its
// words, which may hold a short recipient by chance, aren't looked at.
func refusedRecipient(resp proto.Response, why, r, subject string) bool {
	switch resp.Refused {
	case proto.RefusedRecipient:
		return true
	case proto.RefusedSend:
		return false
	}
	if resp.Error == "" {
		return false
	}
	if strings.Contains(why, r) || subject != "" && strings.HasPrefix(why, "a message needs a recipient (") {
		return true
	}
	for _, words := range []string{" has muted you.", " is offline and takes no messages", " is a T3 thread that is settled",
		" is a delegate that another thread started", " from before the bus was encrypted", " so there is no key to encrypt to"} {
		if strings.Contains(why, words) {
			return true
		}
	}
	return false
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// oneLine cuts a status to what the daemon keeps of it (bus.OneLine at 120). The daemon reads a
// request's line only up to a cap, before its token is checked, so a line far longer than it keeps
// could otherwise make a request too long to be read at all. subjectLine cuts a subject shorter
// still, to maxSubject.
func oneLine(s string) string { return bus.OneLine(s, maxLine) }

func subjectLine(s string) string { return bus.OneLine(s, maxSubject) }

// statusLine is the status a session sets, cut to the line the daemon keeps (oneLine), after it
// is checked for secrets as written: only the cleaned line goes to the daemon, and dropping a
// zero-width space can join a key to the next word, which the daemon's check of the cleaned line
// then misses (#1048), as for a subject (subjectAndBody).
func statusLine(doing string) (string, error) {
	if err := scrub.Check(doing); err != nil {
		return "", err
	}
	return oneLine(doing), nil
}

// subjectAndBody is what a send carries of the subject and body an agent gave. A subject is cut
// to one line of maxSubject characters, so a send with no body and a longer subject, or one of several
// lines, would lose the rest without a word (#1021). It is taken as the whole text instead, and
// subjectFor makes it a subject and body. A send with a body would lose the rest of such a subject
// the same way (#1049), so what subjectFor leaves of the subject goes at the top of the body, where
// a turn shows it and hides a subject that only repeats it (view.RepeatsSubject). A subject whose
// lines joined fit in one loses nothing and is joined, as before. Only if the body would go over
// the daemon's cap is the subject cut as before, rather than a send that went out refused. A body
// is blank as the daemon judges one, after the invisible characters it drops, and only if the
// daemon would take it: one over its cap is the agent's to hear about (#1037).
//
// The subject is checked for secrets here as the agent wrote it, as only its cleaned line goes to
// the daemon, which checks that. The daemon checks a body both ways, as dropping a zero-width space
// can join a key to the next word, so that AKIA…EXAMPLE<U+200B>note ends in no word boundary: as a
// subject, a key so written went out (#1044). subjectFor checks the first line it makes one of, and
// what it leaves goes in the body, which the daemon checks as written.
func subjectAndBody(subject, body string) (string, string, error) {
	blank := len(body) <= bus.MaxBody && strings.TrimSpace(bus.Text(body, math.MaxInt)) == ""
	cut := func() (string, string, error) {
		if err := scrub.Check(subject); err != nil {
			return "", "", err
		}
		return subjectLine(subject), body, nil
	}
	if subjectLine(subject) == strings.TrimSpace(subject) {
		return cut()
	}
	if blank {
		return subjectFor(subject)
	}
	if utf8.RuneCountInString(bus.OneLine(subject, math.MaxInt)) <= maxSubject {
		return cut() // its lines joined lose nothing
	}
	s, rest, err := subjectFor(subject)
	if err != nil && !errors.Is(err, errTooLong) {
		return "", "", err
	}
	if err != nil || s == "" {
		return cut()
	}
	if rest == "" {
		return s, body, nil
	}
	if joined := rest + "\n\n" + body; len(joined) <= bus.MaxBody {
		return s, joined, nil
	}
	return cut()
}

// errTooLong is subjectFor's refusal of a text whose body would be over the daemon's cap. The
// daemon's own would name a body the agent never gave (#1037).
var errTooLong = fmt.Errorf("not sent: the message is over %d KB, and the bus keeps everything forever; split it into several messages", bus.MaxBody>>10)

// subjectFor makes the subject of a message an agent wrote as one text, and the body that goes
// with it: `mur send TO TEXT`, and a send tool call whose subject is the whole text (subjectAndBody).
// It splits the text as the page splits a person's (web.SplitText): a first line that fits is the
// subject and the rest is the body; otherwise the opening words are the subject and the body has
// the whole text. The first line is the first with something the daemon keeps, and it fits or not
// as the daemon keeps it, without the invisible characters it drops, which take no room (#1037).
// The body is the text as written, not as cleaned: the daemon checks it for secrets both ways, as
// dropping a zero-width space can join a key to the next word. A first line that ends in "…" and
// starts the rest, as a cut one would, keeps the whole text in the body, as a turn shows the body
// alone then (view.RepeatsSubject). Every agent's send that needs a subject made gets it here, so
// making it another way, such as with a small model (#1023), is a change to this function alone.
func subjectFor(text string) (subject, body string, err error) {
	whole := strings.TrimSpace(text)
	first, rest := "", text
	for rest != "" && oneLine(first) == "" {
		first, rest = cutLine(rest)
	}
	if err := scrub.Check(first); err != nil {
		return "", "", err // as subjectAndBody does
	}
	// Cleaned as the daemon stores it, a malformed byte as U+FFFD, so RepeatsSubject below sees
	// the subject a turn will.
	if subject = bus.OneLine(bus.Text(first, math.MaxInt), math.MaxInt); utf8.RuneCountInString(subject) <= maxSubject {
		body = strings.TrimSpace(rest)
	} else {
		subject, _ = web.SplitText(subject) // the opening words, cut where a word ends
		body = whole
	}
	if view.RepeatsSubject(subject, strings.TrimSpace(bus.Text(body, math.MaxInt))) {
		body = whole
	}
	if len(body) > bus.MaxBody {
		return "", "", errTooLong
	}
	return subject, body, nil
}

// maxSubject is the longest subject a send makes: what fits on one line of a message on the page
// in the app's default window (#876), as web.SplitText cuts a person's. maxLine is the longest
// line the daemon keeps, of a status or a subject, which older senders' subjects still reach.
const (
	maxSubject = 72
	maxLine    = 120
)

// cutLine cuts s at its first line break, of any kind bus.Text reads as one.
func cutLine(s string) (line, rest string) {
	i := strings.IndexFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029
	})
	if i < 0 {
		return s, ""
	}
	_, n := utf8.DecodeRuneInString(s[i:])
	if strings.HasPrefix(s[i:], "\r\n") {
		n = 2
	}
	return s[:i], s[i+n:]
}

// fullPath makes a path a send names a full one, from this server's working directory, which is
// the agent's: the daemon that reads the files runs somewhere else. On Windows an agent in Git Bash
// writes C:\Users as /c/Users, which is put back. Any other path there that starts at a root with
// no drive, such as Git Bash's /tmp, is left as it is, and the daemon refuses it as no full path:
// Windows would read it on the current drive, where another file of that name may be.
func fullPath(p string, windows bool) string {
	if windows && len(p) >= 3 && p[0] == '/' && p[2] == '/' &&
		(p[1] >= 'a' && p[1] <= 'z' || p[1] >= 'A' && p[1] <= 'Z') {
		p = p[1:2] + ":" + p[2:]
	} else if windows && (strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) && !strings.HasPrefix(p, `\\`)) {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// fullPaths is fullPath for each path a send names.
func fullPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, fullPath(p, runtime.GOOS == "windows"))
	}
	return out
}

func result(render func(proto.Response) string, resp proto.Response, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		r := text(err.Error())
		r.IsError = true
		return r, nil, nil
	}
	return text(render(resp)), nil, nil
}
