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
// this device's person, then a person in another device's presence, then anyone this daemon has seen
// with that name, which reaches a person whose daemon has gone: their device reads the message when
// it comes back. The last three are for names from before people were in the registry, and for a
// daemon that hasn't read it yet. Caller holds mu.
func (d *Daemon) resolvePersonLocked(name string) (string, bool) {
	if addr := d.st.PersonClaims[name]; addr != "" {
		return addr, true
	}
	if name == d.st.Person && name != "" {
		return d.personAddr(), true
	}
	for _, dev := range sortedKeys(d.peers) {
		for _, s := range d.peers[dev].pres.Sessions {
			if s.ID == bus.PersonSession && s.Name == name {
				return bus.Addr(dev, s.ID), true
			}
		}
	}
	addr, ok := d.st.People[name]
	return addr, ok
}

// personNameOfLocked is the name of the person at addr, "" if addr isn't a person's or their name
// isn't known. A person who changed their name has the earlier one in People too, so presence, which
// has only the current one, goes first. Caller holds mu.
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
				return s.Name
			}
		}
	}
	found := ""
	for name, a := range d.st.People {
		if a == addr && (found == "" || name < found) {
			found = name
		}
	}
	return found
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
	if addr, ok := d.resolvePersonLocked(name); ok && addr != d.personAddr() {
		dev, _ := bus.SplitAddr(addr)
		return fmt.Sprintf("%s is taken, by the person on %s.", name, dev)
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
		return web.Account{Suggested: d.suggestedNameLocked()}
	}
	a := web.Account{Name: d.st.Person, Addr: d.personAddr(), Taken: d.nameTakenByLocked()}
	if a.Taken != "" {
		a.Suggested = d.suggestedNameLocked() // the page offers it in place of the taken name
	}
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
		err := d.pushNames(ctx, nameWork{person: name})
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
