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
	// GroupAdd is an agent adding another session to groups (#184): Add is the groups' tags and To
	// the session, whose device applies it as the session's own tags. It is a type of its own
	// because every daemon applies Tags as a human's choice, which the session can't undo, but for
	// leaving a group (#539). A daemon
	// from before groups doesn't know the type and passes over it.
	GroupAdd = "group.add"
	// Boxed is an event encrypted for the team's devices (#156): it holds another event whole, and
	// says nothing else. A daemon from before encryption doesn't know the type and passes over it.
	Boxed = "box"
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

// ValidNick accepts a device's nickname (#190). It is shown where sessions' names are, and is
// often a person's name, so it has that form: never a session's name.
func ValidNick(nick string) bool { return ValidPerson(nick) }

// Event is one entry in a device's log. Fields beyond the first five depend on Type.
type Event struct {
	V    int       `json:"v"`
	ID   string    `json:"id"`
	Type string    `json:"type"`
	From string    `json:"from"` // device/session
	At   time.Time `json:"at"`   // the writer's clock; for display, never for ordering

	Agent  string `json:"agent,omitempty"`  // session.started
	Branch string `json:"branch,omitempty"` // session.started

	To      string `json:"to,omitempty"` // message: device/session, device, "*", or a group's tag
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
	// Person is a person's name on a message they sent, so that a daemon that never saw their
	// presence still calls them by it.
	Person string `json:"person,omitempty"`

	// tags: the tags the human added and removed. To is the session. group.add: Add is the groups
	// an agent added the session to.
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`

	// box: the event, encrypted. A boxed event carries nothing else but V and Type.
	Box *Box `json:"box,omitempty"`
}

// Subjects of encrypted records. A commit's subject used to repeat its content, readable by anyone
// who could read the repo; an encrypted record's says only what it is.
const (
	EventSubject    = "event"
	PresenceSubject = "presence"
)

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
	case GroupAdd:
		return fmt.Sprintf("%s added %s to %s", e.From, e.To, strings.Join(e.Add, ", "))
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
	case GroupAdd:
		if !strings.Contains(e.To, "/") || !validAddr(e.To) || IsPerson(e.To) || len(e.Add) == 0 || len(e.Remove) > 0 || !validTags(e.Add) {
			return false
		}
		for _, t := range e.Add {
			if !tags.IsGroup(t) {
				return false
			}
		}
		return true
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
// Murmur wouldn't write. A device has one person, at one address, so only the first is kept: the
// others would only hold names their device can't be reached by.
func (p *Presence) Clean() {
	for name, id := range p.Names {
		if !names.Valid(name) || !ValidName(id) {
			delete(p.Names, name)
		}
	}
	p.Host = OneLine(p.Host, 64)
	p.Owner = OneLine(p.Owner, 64)
	if !ValidNick(p.Nick) || p.Nick == p.Device {
		p.Nick = ""
	}
	var keep []SessionInfo
	person := false
	for _, s := range p.Sessions {
		if ValidName(s.ID) {
			if s.ID == PersonSession {
				if !ValidPerson(s.Name) || person {
					continue // no name, no way to tell them from a stray session; or a second person
				}
				person = true
				s.Agent = AgentPerson
			} else if !names.Valid(s.Name) {
				s.Name = ""
			}
			s.Agent = OneLine(s.Agent, 40)
			s.Branch = OneLine(s.Branch, 100)
			s.Doing = OneLine(s.Doing, 120)
			s.Committed = cleanBranches(s.Committed)
			s.Tags = tags.CleanRead(s.Tags)
			s.Muted = cleanMuted(s.Muted)
			if s.ID == PersonSession {
				s.Tags = tags.Person() // by the address, whatever the presence says (#267)
			}
			keep = append(keep, s)
		}
	}
	p.Sessions = keep
}

// cleanMuted holds a session's mutes to what Murmur writes: sessions' names, none repeated, at
// most MaxMuted.
func cleanMuted(in []string) []string {
	var out []string
	for _, n := range in {
		if names.Valid(n) && !slices.Contains(out, n) && len(out) < MaxMuted {
			out = append(out, n)
		}
	}
	return out
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

// IsGroup says to is a group's address: its tag, such as group:ui (#184).
func IsGroup(to string) bool { return tags.IsGroup(to) }

// ValidTo accepts a recipient: everyone, a group, a device, or a session.
func ValidTo(to string) bool {
	if to == "*" || IsGroup(to) {
		return true
	}
	if !strings.Contains(to, "/") {
		return ValidDevice(to)
	}
	return validAddr(to)
}

// Presence is a device's current state, the whole of its presence ref.
type Presence struct {
	V      int    `json:"v"`
	Device string `json:"device"`
	Host   string `json:"host,omitempty"`  // absent when encrypted
	Owner  string `json:"owner,omitempty"` // git user.name on that clone
	// Nick is the nickname the device's user gave it (#190), "" if none. Nothing keeps two devices
	// from saying the same one: it is a label, and the ID stays the device's address.
	Nick     string        `json:"nick,omitempty"`
	At       time.Time     `json:"at"` // the writer's clock
	Sessions []SessionInfo `json:"sessions"`
	// Offline lists the IDs of the device's sessions whose T3 thread is settled (#189). They are
	// not in Sessions, and a daemon that reads this refuses to send them a message; one from
	// before the field sees them only as gone, and the writer drops what it sends them.
	Offline []string `json:"offline,omitempty"`
	// Groups says the device's daemon gives its sessions what is sent to a group they are in, and
	// takes an agent's add to one (#184). A sender is told of a member whose device doesn't.
	Groups bool `json:"groups,omitempty"`
	// Epoch is the newest team key the device had read from the team ref when it wrote this, and
	// Holds the team keys it held then, by TeamID (#156). A teammate grants the team keys by them.
	// Both are inside the box, so they are absent from an encrypted presence until it is opened.
	Epoch string   `json:"epoch,omitempty"`
	Holds []string `json:"holds,omitempty"`
	// BoxKey is the device's public box key, which its teammates encrypt to (#156). A presence
	// that carries one is sealed, which makes the key the device's.
	BoxKey string `json:"boxKey,omitempty"`
	// Box holds Host, Owner, Nick, Sessions, Offline and Groups, encrypted for the team's devices; those fields
	// are then empty. Opened, it is nil.
	Box *Box `json:"box,omitempty"`
	// Names is, beside a Box, each session's name with the session's ID, readable: a device that
	// can't open the box (it joined while this one couldn't push) still knows those names are taken,
	// and never gives one out again. The name registry holds the same in the clear.
	Names map[string]string `json:"names,omitempty"`
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
	// HookOff says Murmur's hook doesn't run for the session (proto.SessionView.HookOff, #225). A
	// daemon from before it ignores it.
	HookOff bool `json:"hookOff,omitempty"`
	// Muted is the names of the sessions this one has muted (#27), at most MaxMuted: it gets
	// nothing they send. A daemon from before mutes ignores it, and still delivers to this
	// session's device, which is the one that drops the message.
	Muted []string `json:"muted,omitempty"`
}

// MaxMuted caps SessionInfo.Muted.
const MaxMuted = 50

// MaxCommitted caps SessionInfo.Committed.
const MaxCommitted = 5

// MaxSessions caps the sessions a presence read from the bus may list. Murmur's lists what one
// device runs, far fewer. A reader takes each session name in a presence as used, for good, so one
// that lists more isn't cut to the cap but read as unreadable, whole.
const MaxSessions = 200

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
// characters (a terminal escape, say) into spaces, drops invisible characters (see dropFormat and
// dropInvisible), and cuts s to at most max runes.
func OneLine(s string, max int) string {
	s = dropFormat(dropInvisible(s))
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// Text keeps a text's lines but writes every kind of line break as "\n", turns other control
// characters but tabs into spaces, drops invisible characters (see dropFormat and dropInvisible),
// and cuts it to at most max bytes. Whoever shows it line by line then sees the same lines everyone
// else does.
func Text(s string, max int) string {
	s = dropFormat(dropInvisible(s))
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

// dropFormat removes Unicode format characters (category Cf), which take no space on screen: bidi
// overrides and isolates, which make a terminal or a page show text in another order than it
// reads; zero-width spaces, word joiners and byte-order marks; the soft hyphen; and the tag block
// (U+E0000 to U+E007F), which spells out text a model reads and no terminal or page shows. All of
// it reaches agents' context, so text from the bus could otherwise say one thing to the person
// watching a session and another to its agent.
//
// The zero-width joiner and non-joiner stay where each joins two visible characters outside ASCII:
// the joiner is how emoji such as a family or a woman at a laptop are made, and both shape words in
// Persian and the Indic scripts. No emoji sequence or script has one next to an ASCII character, so
// one there is dropped, and a key can't hide from scrub behind it. So is one in a run of them, or
// next to a space or another format character.
//
// What that costs: flags spelled with tag characters (England's, Scotland's) lose their tags and
// show as a black flag; the direction marks (U+200E, U+200F, U+061C), which mixed Hebrew or Arabic
// and Latin text sometimes needs to show in the right order, are dropped; so are the Arabic number
// signs (U+0600 to U+0605); and a Malayalam chillu in the old encoding, a virama and a joiner at the
// end of a word, loses its joiner.
//
// Other invisible characters aren't format characters: dropInvisible takes those.
func dropFormat(s string) string {
	if !strings.ContainsFunc(s, isFormat) {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		if !isFormat(r) {
			b.WriteRune(r)
			continue
		}
		if (r == '\u200c' || r == '\u200d') && i > 0 && i+1 < len(rs) && joinable(rs[i-1]) && joinable(rs[i+1]) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isFormat(r rune) bool { return unicode.Is(unicode.Cf, r) }

// dropInvisible removes the characters that show nothing but aren't format characters, so
// dropFormat keeps them (#349). It runs before dropFormat, so a joiner that stood next to a selector
// is judged by what is left around it.
//
//   - Variation selectors (U+FE00 to U+FE0F) are marks. One after an emoji or a symbol picks how it
//     is drawn, and stays. A run of them after one character would encode a byte each, so only the
//     first stays. One after ASCII stays only in a keycap (a digit, # or *, then U+20E3, which draws
//     the key), and one after a mark, a joiner, a space, a line break or nothing is dropped: no
//     emoji is spelled so, and a key split by one would hide from scrub, as the joiners would (see
//     dropFormat).
//   - The supplement (U+E0100 to U+E01EF) picks among a CJK ideograph's variant glyphs, so one stays
//     after a Han character and none anywhere else.
//   - Mongolian's free variation selectors (U+180B to U+180D, U+180F) pick a letter's shape, so one
//     stays after a Mongolian letter and none anywhere else.
//   - The combining grapheme joiner (U+034F) changes only how text is sorted and searched, never
//     what is drawn, so it always goes. So do the Khmer inherent vowels (U+17B4, U+17B5), which
//     Unicode advises against using, and the code points set aside to show as nothing that have no
//     meaning yet (U+2065, U+FFF0 to U+FFF8, U+E0000, U+E0002 to U+E001F, U+E0080 to U+E00FF,
//     U+E01F0 to U+E0FFF).
//   - The Hangul fillers (U+115F, U+1160, U+3164, U+FFA0) stand in for a missing part of a Korean
//     syllable, and are kept only next to Hangul that isn't a filler. Elsewhere one shows as a blank
//     that isn't a space, and a run of them as nothing a reader can count.
//
// What that costs: a standardized variant after an ASCII character, such as a zero with a slash
// (U+0030 U+FE00), loses its selector and shows as a plain zero; so does an ideograph's variant
// after a character that isn't Han.
func dropInvisible(s string) string {
	if !strings.ContainsFunc(s, invisible) {
		return s
	}
	rs := []rune(s)
	out := make([]rune, 0, len(rs))
	for i, r := range rs {
		prev, next := rune(-1), rune(-1)
		if len(out) > 0 {
			prev = out[len(out)-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch {
		case r == '\u034f':
			continue
		case isSelector(r):
			if !selectable(prev) {
				continue
			}
			if prev <= unicode.MaxASCII && !(keycap(prev) && next == '\u20e3') {
				continue
			}
		case isIdeographSelector(r):
			if prev < 0 || !unicode.Is(unicode.Han, prev) {
				continue
			}
		case isMongolianSelector(r):
			// Not after the vowel separator (U+180E), which is Mongolian but a format character:
			// dropFormat takes it afterwards, and would leave the selector next to whatever came before.
			if prev < 0 || !unicode.Is(unicode.Mongolian, prev) || isMongolianSelector(prev) || isFormat(prev) {
				continue
			}
		case unusedInvisible(r):
			continue
		case isFiller(r):
			if !hangul(prev) && !hangul(next) {
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

func invisible(r rune) bool {
	return r == '\u034f' || isSelector(r) || isIdeographSelector(r) || isFiller(r) || isMongolianSelector(r) || unusedInvisible(r)
}

// isMongolianSelector is one of Mongolian's free variation selectors (U+180E, the vowel separator,
// is a format character, which dropFormat takes).
func isMongolianSelector(r rune) bool { return r >= 0x180B && r <= 0x180D || r == 0x180F }

// unusedInvisible is a character Unicode says to show as nothing that no text needs: the Khmer
// inherent vowels, which Unicode advises against using, and code points set aside as invisible but
// not yet given a meaning.
func unusedInvisible(r rune) bool {
	return r == 0x17B4 || r == 0x17B5 || r == 0x2065 || r >= 0xFFF0 && r <= 0xFFF8 || r == 0xE0000 ||
		r >= 0xE0002 && r <= 0xE001F || r >= 0xE0080 && r <= 0xE00FF || r >= 0xE01F0 && r <= 0xE0FFF
}

// selectable says whether a variation selector may follow r: a character that starts what is drawn,
// not a mark, a joiner or another selector. Only one may follow it, and a mark between would let a
// run through as mark, selector, mark, selector; no emoji or standardized variant is spelled so,
// and the selector of an emoji joined to another comes before the joiner (U+2764 U+FE0F U+200D).
func selectable(r rune) bool {
	return r >= 0 && !unicode.IsSpace(r) && !unicode.IsControl(r) && !isFormat(r) && !unicode.Is(unicode.M, r)
}

// keycap says whether r is one of the ASCII characters a keycap emoji is made of: 0 to 9, # and *.
func keycap(r rune) bool { return r >= '0' && r <= '9' || r == '#' || r == '*' }

func isSelector(r rune) bool          { return r >= 0xFE00 && r <= 0xFE0F }
func isIdeographSelector(r rune) bool { return r >= 0xE0100 && r <= 0xE01EF }
func isFiller(r rune) bool            { return r == 0x115F || r == 0x1160 || r == 0x3164 || r == 0xFFA0 }

// hangul says whether r is Hangul a filler may stand next to: a syllable or a jamo, not a filler.
func hangul(r rune) bool { return r >= 0 && unicode.Is(unicode.Hangul, r) && !isFiller(r) }

// joinable says whether r is a visible character outside ASCII that a joiner may stand next to: a
// letter, a mark (a vowel sign, an emoji's variation selector), a digit or a symbol (an emoji).
func joinable(r rune) bool {
	return r > unicode.MaxASCII && unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.S)
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
