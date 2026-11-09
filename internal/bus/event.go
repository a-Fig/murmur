// Package bus is the wire format and the git plumbing of the bus: events and presence encoded
// as commit messages on refs/murmur/*, written and read without ever touching a working tree.
package bus

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/tags"
)

// Version is the format version every event and presence record carries. A reader skips
// records with a version it doesn't know rather than guessing at them.
const Version = 1

// Event types.
const (
	SessionStarted = "session.started"
	SessionStopped = "session.stopped"
	Message        = "message"
	// Tags is a human's change to the tags of a session on another device (#198), made on the
	// page of the device whose log carries it. The session's device applies it; a daemon from before
	// tags doesn't know the type and passes over it.
	Tags = "tags"
)

// PersonSession is the session ID of a device's person (#242), so a person's address is
// device/human. It is one ID per device, like the CLI's, and no agent's session can register it.
// AgentPerson is what presence gives as such a session's agent, so a daemon from before people
// lists them in who with their address.
const (
	PersonSession = "human"
	AgentPerson   = "person"
)

// IsPerson says addr is a person's address.
func IsPerson(addr string) bool {
	_, sess := SplitAddr(addr)
	return sess == PersonSession
}

// ValidPerson accepts a person's name: one names.ValidPerson takes, and that send can take as a
// recipient, which a name no filesystem can hold as a device's ref can't be.
func ValidPerson(name string) bool {
	return names.ValidPerson(name) && ValidDevice(name)
}

// Event is one entry in a device's log. Fields beyond the first five depend on Type.
type Event struct {
	V    int       `json:"v"`
	ID   string    `json:"id"`
	Type string    `json:"type"`
	From string    `json:"from"` // device/session
	At   time.Time `json:"at"`   // the writer's clock; for display, never for ordering

	Agent  string `json:"agent,omitempty"`  // session.started
	Branch string `json:"branch,omitempty"` // session.started

	To      string `json:"to,omitempty"` // message: device/session, device, or "*"
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
	// Person is a person's name on a message they sent, so that a daemon that never saw their
	// presence still calls them by it.
	Person string `json:"person,omitempty"`

	// tags: the tags the human added and removed. To is the session.
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// Line is the event as one readable line, used as the commit subject so that `git log` on a
// bus ref reads like a log.
func (e Event) Line() string {
	switch e.Type {
	case SessionStarted:
		return fmt.Sprintf("%s started (%s, %s)", e.From, e.Agent, e.Branch)
	case SessionStopped:
		return e.From + " stopped"
	case Message:
		return fmt.Sprintf("%s -> %s: %s", e.From, e.To, e.Subject)
	case Tags:
		var ch []string
		for _, t := range e.Add {
			ch = append(ch, "+"+t)
		}
		for _, t := range e.Remove {
			ch = append(ch, "-"+t)
		}
		return fmt.Sprintf("%s tags %s: %s", e.From, e.To, strings.Join(ch, " "))
	}
	return e.From + " " + e.Type
}

// Valid reports whether a decoded event is one this version understands. A type it doesn't
// know, from a newer version or an older one, is not: readers pass over it and read on.
//
// Anyone with push access can write the bus, and what it carries reaches agents' context, so a
// reader holds each event to what Murmur itself writes: IDs and addresses in their own alphabets
// (Valid), text fields on one line at Murmur's lengths (Clean, which goes first).
func (e Event) Valid() bool {
	if e.V != Version || !validID(e.ID) || !validAddr(e.From) {
		return false
	}
	switch e.Type {
	case SessionStarted, SessionStopped:
		return true
	case Message:
		return ValidTo(e.To) && e.Subject != ""
	case Tags:
		return strings.Contains(e.To, "/") && validAddr(e.To) && len(e.Add)+len(e.Remove) > 0 &&
			validTags(e.Add) && validTags(e.Remove)
	}
	return false
}

// Clean puts each text field of an event read from the bus on one line, at most as long as Murmur
// itself writes it. Bodies keep their lines; the reader's view marks them as quoted.
func (e *Event) Clean() {
	e.Subject = OneLine(e.Subject, 120)
	e.Agent = OneLine(e.Agent, 40)
	e.Branch = OneLine(e.Branch, 100)
	e.Body = Text(e.Body, MaxBody)
	if e.Type != Message || !IsPerson(e.From) || !ValidPerson(e.Person) {
		e.Person = ""
	}
}

// Clean does for a presence what Event.Clean does for an event, and drops sessions whose IDs
// Murmur wouldn't write.
func (p *Presence) Clean() {
	p.Host = OneLine(p.Host, 64)
	p.Owner = OneLine(p.Owner, 64)
	var keep []SessionInfo
	for _, s := range p.Sessions {
		if ValidName(s.ID) {
			if s.ID == PersonSession {
				if !ValidPerson(s.Name) {
					continue // no name, no way to tell them from a stray session
				}
				s.Agent = AgentPerson
			} else if !names.Valid(s.Name) {
				s.Name = ""
			}
			s.Agent = OneLine(s.Agent, 40)
			s.Branch = OneLine(s.Branch, 100)
			s.Doing = OneLine(s.Doing, 120)
			s.Committed = cleanBranches(s.Committed)
			s.Tags = tags.CleanRead(s.Tags)
			if s.ID == PersonSession {
				s.Tags = tags.Person() // by the address, whatever the presence says (#267)
			}
			keep = append(keep, s)
		}
	}
	p.Sessions = keep
}

// cleanBranches holds a list of branch names to what Murmur writes: each on one line, none empty or
// repeated, at most MaxCommitted.
func cleanBranches(in []string) []string {
	var out []string
	for _, b := range in {
		if b = OneLine(b, 100); b != "" && !slices.Contains(out, b) && len(out) < MaxCommitted {
			out = append(out, b)
		}
	}
	return out
}

// ValidName accepts device and session IDs: lowercase letters, digits and dashes.
func ValidName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ValidDevice accepts device IDs: valid names that every filesystem can hold as a ref (none of
// Windows' reserved device names), other than Murmur's own, under which a message would pass for
// the tool's own words. IDs Murmur makes always pass: a hostname slug and 4 hex digits.
func ValidDevice(s string) bool {
	if !ValidName(s) || s == brand.Name {
		return false
	}
	switch s {
	case "con", "prn", "aux", "nul":
		return false
	}
	if len(s) == 4 && (strings.HasPrefix(s, "com") || strings.HasPrefix(s, "lpt")) && s[3] >= '0' && s[3] <= '9' {
		return false
	}
	return true
}

// validTags accepts the tags of a tags event: tags, at most a session's worth.
func validTags(ts []string) bool {
	if len(ts) > tags.MaxPerSession {
		return false
	}
	for _, t := range ts {
		if !tags.Valid(t) {
			return false
		}
	}
	return true
}

// validID accepts event IDs: lowercase hex digits, as NewID makes them.
func validID(s string) bool {
	if len(s) < 8 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// validAddr accepts a sender: device/session.
func validAddr(a string) bool {
	dev, sess := SplitAddr(a)
	return ValidDevice(dev) && ValidName(sess)
}

// ValidTo accepts a recipient: everyone, a device, or a session.
func ValidTo(to string) bool {
	if to == "*" {
		return true
	}
	if !strings.Contains(to, "/") {
		return ValidDevice(to)
	}
	return validAddr(to)
}

// Presence is a device's current state, the whole of its presence ref.
type Presence struct {
	V        int           `json:"v"`
	Device   string        `json:"device"`
	Host     string        `json:"host"`
	Owner    string        `json:"owner,omitempty"` // git user.name on that clone
	At       time.Time     `json:"at"`              // the writer's clock
	Sessions []SessionInfo `json:"sessions"`
	// Offline lists the IDs of the device's sessions whose T3 thread is settled (#189). They are
	// not in Sessions, and a daemon that reads this refuses to send them a message; one from
	// before the field sees them only as gone, and the writer drops what it sends them.
	Offline []string `json:"offline,omitempty"`
}

// SessionInfo is one session as its device's presence describes it.
type SessionInfo struct {
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"` // two words, like quiet-otter; absent until assigned
	Agent   string    `json:"agent"`
	Branch  string    `json:"branch,omitempty"`
	Doing   string    `json:"doing,omitempty"`
	Started time.Time `json:"started"`
	// LastActive is the writer's clock at the session's last tool call, or its last request to the
	// daemon; absent when the session has made none, or on a daemon from before #21. It goes out
	// with whatever presence write comes next, never as a write of its own.
	LastActive time.Time `json:"lastActive,omitzero"`
	// Committed is the branches the session has committed to, most recent first, at most
	// MaxCommitted. The commits, their SHAs and the worktree stay on the session's device.
	Committed []string `json:"committed,omitempty"`
	// Tags is the session's tags as its device decided them, each with where it came from (#198).
	// A daemon from before tags ignores them.
	Tags []tags.Tag `json:"tags,omitempty"`
}

// MaxCommitted caps SessionInfo.Committed.
const MaxCommitted = 5

// Encode renders a record as a commit message: a readable subject line, a blank line, then
// the JSON that readers actually parse.
func Encode(v any, subject string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return OneLine(subject, 120) + "\n\n" + string(b) + "\n", nil
}

// Decode parses a commit message written by Encode.
func Decode(msg string, v any) error {
	i := strings.Index(msg, "\n\n")
	if i < 0 {
		return errors.New("no JSON body")
	}
	return json.Unmarshal([]byte(strings.TrimSpace(msg[i+2:])), v)
}

// MaxBody caps a message body. Everything on the bus stays in history for good.
const MaxBody = 32 << 10

// OneLine collapses whitespace, line breaks of every kind included, turns other control
// characters (a terminal escape, say) into spaces, and cuts s to at most max runes.
func OneLine(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// Text keeps a text's lines but writes every kind of line break as "\n", turns other control
// characters but tabs into spaces, and cuts it to at most max bytes. Whoever shows it line by line
// then sees the same lines everyone else does.
func Text(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029:
			return '\n'
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}
	return s
}

// Addr joins a device and a session into an address.
func Addr(device, session string) string { return device + "/" + session }

// SplitAddr splits an address; a bare device has an empty session.
func SplitAddr(addr string) (device, session string) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return addr, ""
}

// RandHex returns n random hex digits.
func RandHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)[:n]
}

// NewID returns a fresh event ID.
func NewID() string { return RandHex(8) }
