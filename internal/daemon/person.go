package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// A person's account (#242). A human chooses a name on the page, and from then on is a participant:
// their address is device/human, presence lists them, agents find them in who and send to their
// name, and the messages to and from them are kept on this device past a restart. docs/VISION.md
// ("A person on the page") has the reasons.
//
// The account is one per clone, like the CLI's mailbox it sits beside. It is unlike it in three ways:
// it has a name, what is sent to it reaches the page and only the page, and it gets what was sent
// while the daemon was down, because a person's daemon exits soon after their page closes, and the
// agent they asked may answer hours later.

// maxPersonLog is how many of a person's messages this device keeps. The page shows them all, and
// agents' chatter doesn't push them out.
const maxPersonLog = 2000

func (d *Daemon) personAddr() string { return bus.Addr(d.device, bus.PersonSession) }

// ownPersonMsgLocked says e is to or from this device's person. Caller holds mu.
func (d *Daemon) ownPersonMsgLocked(e bus.Event) bool {
	addr := d.personAddr()
	return e.From == addr || e.To == addr
}

// resolvePersonLocked finds who is called name: whoever claimed it in the names registry (#268), then
// this device's person, then the first person this daemon saw with that name (People), which reaches
// a person whose daemon has gone: their device reads the message when it comes back. Then a person in
// another device's presence, for a state from before People held everyone presence shows. The last
// three are for names from before people were in the registry, and for a daemon that hasn't read it
// yet. The first sighting goes before presence because any device can say its person has any name,
// and the first to be seen with one keeps it, as with sessions' names. Caller holds mu.
func (d *Daemon) resolvePersonLocked(name string) (string, bool) {
	if addr := d.st.PersonClaims[name]; addr != "" {
		return addr, true
	}
	if name == d.st.Person && name != "" {
		return d.personAddr(), true
	}
	if addr, ok := d.st.People[name]; ok {
		return addr, true
	}
	for _, dev := range sortedKeys(d.peers) {
		for _, s := range d.peers[dev].pres.Sessions {
			if s.ID == bus.PersonSession && s.Name == name {
				return bus.Addr(dev, s.ID), true
			}
		}
	}
	return "", false
}

// listedPersonNameLocked is the name who and the page list a peer's person under: the one its
// presence gives, if that name reaches them, and "" otherwise, when they are listed by address. Any
// device can say its person has any name, so one whose presence says ada while ada is someone
// else's would be listed as a second ada (#350); sending to ada reaches whoever holds the name, the
// way a message's name counts only if its sender holds it (personSenderLocked). Caller holds mu.
func (d *Daemon) listedPersonNameLocked(addr, name string) string {
	if name == "" {
		return ""
	}
	if got, ok := d.resolvePersonLocked(name); ok && got != addr {
		return ""
	}
	return name
}

// personNameOfLocked is the name of the person at addr, "" if addr isn't a person's or their name
// isn't known. A person who changed their name has the earlier one in People too, so presence, which
// has only the current one, goes first. Another device's person has a name only if it reaches them
// (listedPersonNameLocked), so a lookup, a turn or the page never labels them, or a message to
// them, with a name that is someone else's (#350). Caller holds mu.
func (d *Daemon) personNameOfLocked(addr string) string {
	if !bus.IsPerson(addr) {
		return ""
	}
	dev, _ := bus.SplitAddr(addr)
	if dev == d.device {
		return d.st.Person
	}
	if p := d.peers[dev]; p != nil {
		for _, s := range p.pres.Sessions {
			if s.ID == bus.PersonSession {
				if name := d.listedPersonNameLocked(addr, s.Name); name != "" {
					return name
				}
			}
		}
	}
	found := ""
	for name, a := range d.st.People {
		if a == addr && (found == "" || name < found) && d.listedPersonNameLocked(addr, name) != "" {
			found = name
		}
	}
	return found
}

// personSenderLocked is the name a person's message e is shown from: the one it carries, which may
// be an earlier name of theirs, or else the one their device gives them. A message can carry any
// name, so the one it carries counts only if its sender holds it: by a claim in the registry, in
// its live presence, or in People, as a name they have had. People has only what a presence or a
// claim gave it, never a message's name (notePersonLocked), so this holds for a sender whose
// presence has gone, as one that renamed before #272 and has no claim. Either name then
// counts only if it doesn't reach someone else; otherwise the message is from its address. onMany
// says a live presence of another device says the name too, so whoever reads the message should be
// told which device sent it; it lasts only as long as that presence does. Caller holds mu.
func (d *Daemon) personSenderLocked(e bus.Event) (name string, onMany bool) {
	carried := e.Person
	if carried != "" && d.st.PersonClaims[carried] != e.From && d.livePersonLocked(e.From) != carried &&
		d.st.People[carried] != e.From {
		carried = ""
	}
	for _, n := range []string{carried, d.personNameOfLocked(e.From)} {
		if addr, ok := d.resolvePersonLocked(n); n != "" && (!ok || addr == e.From) {
			return n, d.nameOnOthersLocked(n, e.From)
		}
	}
	return "", false
}

// personInfoLocked is this device's person as presence carries them. Last active is when a page
// someone can see last called, so a green dot means they are looking at it. Caller holds mu.
func (d *Daemon) personInfoLocked() bus.SessionInfo {
	// The tag goes out for daemons from before #267, which show presence's tags as they are. A daemon
	// since works it out from the address.
	info := bus.SessionInfo{ID: bus.PersonSession, Name: d.st.Person, Agent: bus.AgentPerson, Started: d.st.PersonSince.UTC(),
		Tags: tags.Person()}
	if !d.lastPage.IsZero() {
		info.LastActive = d.lastPage.UTC()
	}
	return info
}

// personViewLocked is this device's person as who shows them. Caller holds mu.
func (d *Daemon) personViewLocked() proto.SessionView {
	return proto.SessionView{Addr: d.personAddr(), Name: d.st.Person, Agent: bus.AgentPerson, Started: d.st.PersonSince,
		LastActive: d.lastPage, Tags: tags.Person()}
}

// personAsks says m is a person's message to one session, not to a device or to everyone: only that
// may wake a session that has been idle for long, or a broadcast from the page would wake them all.
func personAsks(m *inMsg) bool {
	return bus.IsPerson(m.Event.From) && strings.Contains(m.Event.To, "/")
}

// personWakesLocked says session id may be woken for a person's message, however long it has been
// idle. Do not disturb and a wake setting of Never are a human's choices too, and hold. Caller holds
// mu.
func (d *Daemon) personWakesLocked(id string, wake pageSettings) bool {
	return !d.dndLocked(id) && !d.offMeshLocked(id) && wake.choice(d.sessionNameLocked(id)).When != web.WakeNever
}

func (d *Daemon) personLogPath() string { return filepath.Join(d.dir, "person-messages.jsonl") }

// loadPersonLog puts the messages kept on disk among the page's, before the first sync, so that a
// read of the log they came from knows them by ID. A line that doesn't read is passed over. A log
// grown to twice what is kept is cut back here, as appending never does.
func (d *Daemon) loadPersonLog() {
	f, err := os.Open(d.personLogPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		d.log.Printf("reading a person's messages: %v", err)
		return
	}
	var events []bus.Event
	lines := bufio.NewScanner(f)
	lines.Buffer(nil, 8*bus.MaxBody) // a line is one JSON-escaped event, at most a body and a subject
	for lines.Scan() {
		var e bus.Event
		if json.Unmarshal(lines.Bytes(), &e) != nil {
			continue
		}
		if e.Clean(); e.Valid() && e.Type == bus.Message {
			events = append(events, e)
		}
	}
	if err := lines.Err(); err != nil {
		d.log.Printf("reading a person's messages: %v", err)
	}
	f.Close() // before rewritePersonLog: Windows won't rename over a file that is open
	long := len(events) > 2*maxPersonLog
	events = events[max(len(events)-maxPersonLog, 0):]
	for _, e := range events {
		d.keepMessageLocked(e)
	}
	d.recoverUnreadLocked(events)
	if long {
		d.rewritePersonLog(events)
	}
}

// recoverUnreadLocked gives the person's inbox what the log holds past the last save. The sync that
// read those messages will read them again, but finds them known by ID and adds nothing, so without
// this they would stay on the page and never count as unread.
func (d *Daemon) recoverUnreadLocked(events []bus.Event) {
	if len(events) == 0 {
		return
	}
	from := 0
	if d.st.PersonLogged != "" {
		at := slices.IndexFunc(events, func(e bus.Event) bool { return e.ID == d.st.PersonLogged })
		if at < 0 {
			at = len(events) - 1 // cut out of the log, so nothing says which came later
		}
		from = at + 1
	}
	for _, e := range events[from:] {
		if e.To == d.personAddr() && e.From != d.personAddr() {
			d.addInboxLocked(bus.PersonSession, e)
		}
	}
	d.st.PersonLogged = events[len(events)-1].ID
}

func (d *Daemon) rewritePersonLog(events []bus.Event) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, e := range events {
		if enc.Encode(e) != nil {
			return
		}
	}
	tmp := d.personLogPath() + ".tmp"
	err := os.WriteFile(tmp, []byte(b.String()), 0o600)
	if err == nil {
		err = os.Rename(tmp, d.personLogPath())
	}
	if err != nil {
		d.log.Printf("cutting back a person's messages: %v", err)
		os.Remove(tmp)
	}
}

// logPersonLocked appends one message to what is kept on disk. Caller holds mu.
func (d *Daemon) logPersonLocked(e bus.Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(d.personLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		d.st.PersonLogged = e.ID
	}
	if err != nil {
		d.log.Printf("keeping a person's message: %v", err)
	}
}

// personNameProblemLocked says why name can't be this device's person's, "" if it can. Caller holds
// mu.
func (d *Daemon) personNameProblemLocked(name string) string {
	if !bus.ValidPerson(name) {
		return fmt.Sprintf("%q can't be a name. Use 2 to %d lowercase letters and digits, with single dashes between words.", name, names.MaxPerson)
	}
	if _, isDevice := d.peers[name]; isDevice || name == d.device {
		return fmt.Sprintf("%s is a device's ID here, so a message to it would go to that device.", name)
	}
	if dev := d.nameTakenLocked(name); dev != "" {
		return fmt.Sprintf("%s is taken, by the person on %s.", name, dev)
	}
	return ""
}

// nameTakenLocked is the device whose person has name, other than this one, "" if none. Once the
// registry has been read by a daemon that keeps people's claims, only a claim there or a live
// presence takes a name: a sighting in People alone may be a presence long gone, and the claim
// decides between two people anyway (#268). Before that read, any sighting counts. Caller holds mu.
func (d *Daemon) nameTakenLocked(name string) string {
	if !d.namesChecked || !d.st.NamesPeople {
		if addr, ok := d.resolvePersonLocked(name); ok && addr != d.personAddr() {
			dev, _ := bus.SplitAddr(addr)
			return dev
		}
		return ""
	}
	switch addr := d.st.PersonClaims[name]; addr {
	case d.personAddr():
		return ""
	case "":
	default:
		dev, _ := bus.SplitAddr(addr)
		return dev
	}
	for _, dev := range sortedKeys(d.peers) {
		if d.livePersonLocked(bus.Addr(dev, bus.PersonSession)) == name {
			return dev
		}
	}
	return ""
}

// suggestedNameLocked is the name to offer a person who has none: their git user name, if it makes
// one nobody has. Caller holds mu.
func (d *Daemon) suggestedNameLocked() string {
	slug := names.PersonSlug(d.owner)
	if slug == "" || d.personNameProblemLocked(slug) != "" {
		return ""
	}
	return slug
}

// accountLocked is this device's person as the page sees them. Caller holds mu.
func (d *Daemon) accountLocked() web.Account {
	if d.st.Person == "" {
		return web.Account{Suggested: d.suggestedNameLocked(), Nick: d.st.Nick}
	}
	a := web.Account{Name: d.st.Person, Addr: d.personAddr(), Taken: d.nameTakenByLocked(), Nick: d.st.Nick}
	for _, m := range d.st.Inbox[bus.PersonSession] {
		a.Unread = append(a.Unread, m.Event.ID)
	}
	return a
}

// nameTakenByLocked is the device whose person claimed this device's person's name in the names
// registry first, "" if nobody else did. That can only be a name chosen before people were in the
// registry (#268), and the page asks them for another. Caller holds mu.
func (d *Daemon) nameTakenByLocked() string {
	addr := d.st.PersonClaims[d.st.Person]
	if d.st.Person == "" || addr == "" || addr == d.personAddr() {
		return ""
	}
	dev, _ := bus.SplitAddr(addr)
	return dev
}

// claimPersonName claims name for this device's person in the names registry, by the same leased
// push that gives sessions their names, so that of two people choosing one name at once, only one
// gets it (#268). A push refused because the registry moved is tried again after reading what moved
// it, up to nameTries times; the error says whether someone else has the name.
func (d *Daemon) claimPersonName(ctx context.Context, name string) error {
	// The page is served before Run's first sync. A sync of its own started first would hold syncMu
	// for up to a minute, and Run's, which a stop can cut short, would wait for it, so the daemon
	// couldn't stop in that time (Sol's review of #377).
	d.awaitFirstSync(ctx)
	d.mu.Lock()
	read := d.namesChecked
	d.mu.Unlock()
	if !read {
		// The registry as this daemon last read it may lack a claim made since, or one an older
		// daemon passed over (see NamesPeople); a claim on top of it would land all the same.
		if err := d.pollOnce(ctx); err != nil {
			return fmt.Errorf("couldn't read the names on the remote: %v", err)
		}
	}
	for range nameTries {
		err := d.pushNames(ctx, nameWork{person: []string{name}})
		d.mu.Lock()
		holder := d.st.PersonClaims[name]
		d.mu.Unlock()
		switch {
		case holder == d.personAddr():
			return nil
		case holder != "":
			dev, _ := bus.SplitAddr(holder)
			return fmt.Errorf("%s is taken, by the person on %s.", name, dev)
		case err == nil:
			return errors.New("the name wasn't claimed; try again")
		case !errors.Is(err, bus.ErrRejected):
			return fmt.Errorf("couldn't claim the name on the remote, so nobody else can take it: %v", err)
		}
		if err := d.pollOnce(ctx); err != nil {
			return fmt.Errorf("couldn't read the names on the remote: %v", err)
		}
	}
	return errors.New("the remote's names were busy; try again")
}

// SetAccount gives this device's person a name, or a new one, once it is claimed in the names
// registry. The name they had still reaches them: it stays theirs in the registry, and in People
// here and on every device that saw it.
func (s pageSource) SetAccount(ctx context.Context, name string) (web.Account, error) {
	d := s.d
	d.mu.Lock()
	d.pageSeenLocked(ctx, time.Now())
	why := d.personNameProblemLocked(name)
	d.mu.Unlock()
	if why != "" {
		return web.Account{}, errors.New(why)
	}
	if err := d.claimPersonName(ctx, name); err != nil {
		return web.Account{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st.Person == "" {
		d.st.PersonSince = time.Now().Round(0)
	}
	d.st.Person = name
	d.st.People[name] = d.personAddr()
	d.saveLocked()
	d.kickPresence()
	return d.accountLocked(), nil
}

// Send sends text as this device's person. It returns once the message is on the remote, as an
// agent's send does.
func (s pageSource) Send(ctx context.Context, to, text string) (web.Sent, error) {
	d := s.d
	d.pageCalled(ctx)
	d.awaitFirstSync(ctx) // as send does for a session: the names it resolves, and its push
	d.mu.Lock()
	named, takenBy := d.st.Person != "", d.nameTakenByLocked()
	d.mu.Unlock()
	if !named {
		return web.Sent{}, errors.New("choose a name first")
	}
	if takenBy != "" {
		// Their messages would carry a name that reaches someone else.
		return web.Sent{}, fmt.Errorf("the person on %s has your name; choose another before you send", takenBy)
	}
	subject, body := web.SplitText(text)
	r := d.send(ctx, d.personAddr(), proto.Request{To: to, Subject: subject, Body: body})
	if r.Error != "" {
		return web.Sent{}, errors.New(r.Error)
	}
	sent := web.Sent{Note: r.Note}
	if len(r.Messages) > 0 {
		sent.ID = r.Messages[0].ID
	}
	return sent, nil
}

// MarkRead takes the messages with these IDs out of the person's unread.
func (s pageSource) MarkRead(ctx context.Context, ids []string) error {
	d := s.d
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pageSeenLocked(ctx, time.Now())
	box := d.st.Inbox[bus.PersonSession]
	kept := slices.DeleteFunc(slices.Clone(box), func(m *inMsg) bool { return slices.Contains(ids, m.Event.ID) })
	if len(kept) == len(box) {
		return nil
	}
	if len(kept) == 0 {
		delete(d.st.Inbox, bus.PersonSession)
	} else {
		d.st.Inbox[bus.PersonSession] = kept
	}
	d.saveLocked()
	return nil
}
