package daemon

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// The page for humans (#22). The daemon serves internal/web on loopback and is its web.Source.
// Everything the page shows is what the daemon already holds in memory, so a poll costs no git
// call and no network, and no lock is held while one is made.

// defaultWakeMinutes is what a wake setting of 0 minutes stands for.
const defaultWakeMinutes = 5

// listenPage opens the page's port. It is never the sessions' port: those carry the daemon's
// protocol, and the page speaks HTTP to a browser. Only 127.0.0.1 will do, not just any loopback
// address: the page answers only requests addressed to 127.0.0.1 or localhost (web's Host check),
// so on [::1] or 127.0.0.2 it would refuse everything sent to the address it gave out.
//
// inherited, if not nil, is the page's socket from the daemon this one replaced, and h that
// daemon's handoff (#255); listenPage closes it. The page goes on there, with the old token, only
// if it is the socket h names: the port was bound all along, so an open tab has sent its token to
// no one else. Otherwise the page gets a new port and keeps the token open made. A port that was
// ever free could have been taken by another user's process, which a tab open on it then sent the
// old token, so that token must not open the page again; only `ui` hands out the new one.
//
// keep is a second hold on the page's socket, which lets it outlive the listener (see keepable).
func (d *Daemon) listenPage(inherited *os.File, h pageHandoff, handed bool) (ln net.Listener, keep *os.File, err error) {
	host, _, err := net.SplitHostPort(d.opt.PageAddr)
	if err == nil && host != "127.0.0.1" {
		err = fmt.Errorf("%s isn't on 127.0.0.1, the loopback address the page answers on", d.opt.PageAddr)
	}
	if err != nil {
		if inherited != nil {
			inherited.Close()
		}
		return nil, nil, err
	}
	if inherited != nil {
		ln, err := d.inheritedPage(inherited, h, handed)
		inherited.Close()
		if err == nil {
			d.pageTok = h.Token
			ln, keep := d.keepable(ln)
			return ln, keep, nil
		}
		d.log.Printf("the page's socket from the daemon before isn't used (%v), so the page moves; a tab open on it needs `%s ui` again", err, brand.Command)
	} else if handed {
		d.log.Printf("the daemon before didn't hand over the page's socket, so the page moves; a tab open on it needs `%s ui` again", brand.Command)
	}
	if ln, err = net.Listen("tcp", d.opt.PageAddr); err != nil {
		return nil, nil, err
	}
	ln, keep = d.keepable(ln)
	return ln, keep, nil
}

// inheritedPage is the listener on f, if f is the socket h names.
func (d *Daemon) inheritedPage(f *os.File, h pageHandoff, handed bool) (net.Listener, error) {
	if !handed {
		return nil, errors.New("it came without the page's address and token")
	}
	if d.opt.PageAddr != defaultPageAddr {
		return nil, fmt.Errorf("this daemon serves the page on %s", d.opt.PageAddr)
	}
	// On Windows the socket is still tied to the completion port of the daemon before, which
	// served the page from it.
	if err := proc.Unpoll(f); err != nil {
		return nil, err
	}
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, err
	}
	if host, _, err := net.SplitHostPort(h.Web); err != nil || host != "127.0.0.1" || ln.Addr().String() != h.Web {
		ln.Close()
		return nil, fmt.Errorf("it is on %s, and the page was on %s", ln.Addr(), h.Web)
	}
	return ln, nil
}

// keepable returns a listener on ln's socket to serve the page from, in place of ln, and a second
// hold on that socket, which keeps the socket bound, and queueing connections, after the listener
// is closed: the page's server closes its listener when it stops, and a daemon restarting onto an
// update hands the socket to the next one (#255). Without a hold, ln is served as it is, and a
// restart moves the page.
//
// The listener is made from the hold rather than being ln itself because on Windows, taking the
// hold detaches ln's socket from the runtime's I/O completion port.
func (d *Daemon) keepable(ln net.Listener) (net.Listener, *os.File) {
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		return ln, nil
	}
	keep, err := tl.File()
	if err == nil {
		var serve net.Listener
		if serve, err = net.FileListener(keep); err == nil {
			ln.Close()
			return serve, keep
		}
		keep.Close()
	}
	d.log.Printf("the page's port can't be held for a restart, which will move it: %v", err)
	return ln, nil
}

// newPage makes the page's server, which asks GitHub about pull requests and issues by running gh
// in the clone. This package's tests put one that asks nothing in its place (#43): a test's
// daemon would run the real gh, as whoever runs the tests, in a clone with no GitHub remote.
var newPage = web.New

// servePage serves the page on ln until ctx is done.
func (d *Daemon) servePage(ctx context.Context, ln net.Listener) {
	page := newPage(pageSource{d}, d.pageTok, repoDir(d.opt.GitDir))
	prefs, err := web.ReadPreferences(d.dir)
	if err != nil {
		d.log.Printf("reading page preferences: %v", err)
	}
	page.WithPreferences(prefs, func(next web.Preferences) error {
		err := writeJSON(filepath.Join(d.dir, web.PreferencesFile), next)
		if err != nil {
			d.log.Printf("saving page preferences: %v", err)
		}
		return err
	})
	if d.opt.Repos != nil {
		page.WithRepos(d.opt.Repos)
	}
	d.page.Store(page)
	// Any local user can connect, and a connection Go keeps open costs the daemon a file descriptor:
	// one that sends a request and then sits idle would be kept for good, and enough of them would
	// leave the daemon none for its sessions or git. A tab polls every 3 s, or 15 s when hidden, so
	// two idle minutes close only a tab's connection that it no longer uses; a request's body is at
	// most 64 KiB. No write timeout, which would also cut off a handler still at work.
	srv := &http.Server{
		Handler:           page,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          d.log,
	}
	d.bg.Add(2)
	go func() {
		defer d.bg.Done()
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			d.log.Printf("the page is no longer served: %v", err)
		}
	}()
	go func() {
		defer d.bg.Done()
		<-ctx.Done()
		// Let a request in flight finish. The slowest is a first call for messages, which waits
		// historyWait at most for the logs to be read back, and awaitHistory lets go of it now.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			srv.Close()
		}
		// The page's PR lookups run gh in the clone; the daemon waits for them as for its other
		// background work, so none is left running there after it exits.
		page.Close()
	}()
}

// pageKeysOut says a key to the page is out, unspent and still good (#348).
func (d *Daemon) pageKeysOut() bool {
	page := d.page.Load()
	return page != nil && page.KeysOut()
}

// repoDir is the clone's top directory, where gh finds the repository from its remotes.
func repoDir(gitDir string) string {
	if filepath.Base(gitDir) == ".git" {
		return filepath.Dir(gitDir)
	}
	return gitDir
}

// pageSource is the daemon as the page sees it. The page's server calls it only for requests that
// carry the page's token, so each call is proof that a page is open; one from a page someone can
// see (web.Watched) keeps the daemon running.
type pageSource struct{ d *Daemon }

var _ web.Source = pageSource{}

// State lists this device and every device the daemon knows, with their sessions. A session has
// its name for an address; one without a name yet, or on a daemon that gives none, has the
// device/session address instead, which `send` takes just as well.
func (s pageSource) State(ctx context.Context) (web.State, error) {
	d := s.d
	var version web.Version
	if d.opt.Version != nil {
		version = d.opt.Version()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	version.Ready = d.updateReady
	version.Daemon = d.started.UTC().Format(time.RFC3339Nano)
	now := time.Now()
	d.pageSeenLocked(ctx, now)

	self := web.Device{ID: d.device, Host: d.host, Owner: d.owner, Nick: d.nickOfLocked(d.device), Self: true, Reachable: true, LastSeen: now}
	if d.st.Person != "" {
		self.Person = &web.Person{Name: d.st.Person, Addr: d.personAddr(), LastActive: d.lastPage, Tags: tags.Person()}
	}
	live := d.liveNamesLocked()
	hidden := d.hidden.Load()
	for _, ss := range d.sortedSessionsLocked() {
		if hidden {
			break // invisible: this device's agents are off Murmur, and the page doesn't show them
		}
		if !ss.dropped.IsZero() && live[ss.name] {
			continue // its name went to the session that took over, which stands for the conversation
		}
		if d.offMeshLocked(ss.id) {
			continue // offline: its T3 thread is settled (#189)
		}
		addr := bus.Addr(d.device, ss.id)
		self.Sessions = append(self.Sessions, web.Session{
			Name: nameOr(ss.name, addr), Addr: addr, Agent: ss.agent, Branch: ss.branch,
			Committed: d.committedLocked(ss.id), Doing: ss.shownDoing(), Worktree: ss.worktree, Started: ss.started,
			LastActive: ss.lastActive, Tags: d.tagsOfLocked(ss), HookOff: d.hookOffLocked(ss)})
	}
	colors := d.colorsLocked()
	self.Color, self.Picked = colors[d.device].Color, colors[d.device].Picked
	self.DND = d.dnd.Load()
	st := web.State{Repo: d.repoName, Device: d.device, Devices: []web.Device{self}, Version: version, Account: d.accountLocked(),
		Mode: d.modeView(), SendFiles: d.pageSend.Load(), ReceiveFiles: d.pageRecv.on.Load(),
		AgentsSendOff: d.agentsSendOff.Load(), AgentsReceiveOff: d.agentsReceiveOff.Load()}
	for _, id := range sortedKeys(d.peers) {
		p := d.peers[id]
		dv := web.Device{ID: id, Host: p.pres.Host, Owner: p.pres.Owner, Nick: d.nickOfLocked(id), Reachable: !d.staleLocked(p),
			LastSeen: p.lastChange, Color: colors[id].Color, Picked: colors[id].Picked, DND: p.pres.DND}
		for _, ps := range p.pres.Sessions {
			if ps.ID == proto.CLISession {
				continue // V1 daemons list their CLI
			}
			addr := bus.Addr(id, ps.ID)
			if ps.ID == bus.PersonSession {
				dv.Person = &web.Person{Name: nameOr(d.listedPersonNameLocked(addr, ps.Name), addr), Addr: addr,
					LastActive: peerActive(p, ps), Tags: ps.Tags}
				continue
			}
			dv.Sessions = append(dv.Sessions, web.Session{
				Name: nameOr(ps.Name, addr), Addr: addr, Agent: ps.Agent, Branch: ps.Branch,
				Committed: ps.Committed, Doing: ps.Doing, Started: peerStarted(p, ps), LastActive: peerActive(p, ps),
				Tags: ps.Tags, HookOff: ps.HookOff})
		}
		st.Devices = append(st.Devices, dv)
	}
	st.Mutes = d.muteViewsLocked()
	st.Receipts = d.receiptsLocked()
	return st, nil
}

// Restart lets an installed update restart the daemon although a page is open: the page asked.
func (s pageSource) Restart(ctx context.Context) error {
	d := s.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.updateReady {
		return web.ErrNoUpdate
	}
	d.restartAsked = true
	return nil
}

func nameOr(name, addr string) string {
	if name != "" {
		return name
	}
	return addr
}

// pageMsg is a message the page can show: the event as the bus carried it, its number, and the ID
// the page knows it by. Names are put in when the page asks, so a name that lands later still
// shows on older messages.
type pageMsg struct {
	seq uint64
	e   bus.Event
	id  string
}

// msgKey says which message e is, for the page's list: its ID, who sent it and when. The ID
// alone used to (#603). An honest ID is 64 random bits and never repeats, but a device can
// write any ID it likes, and a second message with an ID the list held was dropped without a
// word: a device could hide its own later message from every page, and after a restart, when
// the list is read back in the order of the senders' clocks, put a message of its own in the
// place of someone else's by copying the ID onto one with an earlier time. The same event read
// twice, live and from history, has the same sender and time, and is still kept once.
func msgKey(e bus.Event) string {
	return e.ID + "\x00" + e.From + "\x00" + strconv.FormatInt(e.At.UnixNano(), 36)
}

// idKeep is how many dropped messages' IDs stay spoken for. The page keeps 2,000 messages of its
// own, four times what the daemon does, and must not be given another message under an ID it
// still holds.
const idKeep = 4000

// pageIDLocked is the ID the page is to know e by, and notes it. The page keeps its messages by
// ID, and passes over one it has, so an ID means one message for as long as a page may hold it:
// the first message this daemon kept with an ID owns it plain, through a merge that sorts the
// list again and for idKeep dropped messages after it has gone from the list itself. Any other
// message with that ID gets it with a mark of which message it is, the same mark each time, so
// the page shows both. The mark is 64 bits of a hash of what the sender chose, long enough that
// none can be made to match another's. A daemon that restarts starts again: its page loads
// again too, unless an update handed it over. Caller holds mu.
func (d *Daemon) pageIDLocked(e bus.Event) string {
	if d.idOwner == nil {
		d.idOwner = map[string]string{}
	}
	if _, owned := d.idOwner[e.ID]; !owned {
		d.idOwner[e.ID] = msgKey(e)
	}
	return d.pageIDOfLocked(e)
}

// pageIDOfLocked is the ID the page knows e by, or would: its own if it owns it or nothing
// does, else the marked one. Receipts go by the event's ID alone, so the page shows them on the
// message that has it plain. Caller holds mu.
func (d *Daemon) pageIDOfLocked(e bus.Event) string {
	key := msgKey(e)
	if owner, owned := d.idOwner[e.ID]; !owned || owner == key {
		return e.ID
	}
	sum := sha256.Sum256([]byte(key))
	return e.ID + "~" + hex.EncodeToString(sum[:8])
}

// recordMessageLocked keeps a message for the page. Every message this daemon reads goes in,
// whoever it is for: every record is encrypted for the whole team, which could read them all
// before encryption too. One to or from this device's person goes on disk too. Caller holds mu.
func (d *Daemon) recordMessageLocked(e bus.Event) {
	if d.msgKeys[msgKey(e)] {
		return
	}
	d.keepMessageLocked(e)
	if d.ownPersonMsgLocked(e) {
		d.logPersonLocked(e)
	}
}

// keepMessageLocked adds e to the page's messages, dropping the oldest once there are PageMessages.
// A person's own messages are counted apart, up to maxPersonLog, so a busy repo doesn't push a
// person's conversations out from under them. Caller holds mu.
func (d *Daemon) keepMessageLocked(e bus.Event) {
	own := d.ownPersonMsgLocked(e)
	if own {
		d.ownMsgs++
	}
	drop := func(mine bool) {
		i := slices.IndexFunc(d.msgs, func(m pageMsg) bool { return d.ownPersonMsgLocked(m.e) == mine })
		if i < 0 {
			return
		}
		delete(d.msgIDs, d.msgs[i].id)
		delete(d.msgKeys, msgKey(d.msgs[i].e))
		if gone := d.msgs[i]; gone.id == gone.e.ID {
			// Its ID stays its own for a while yet: a page still has it (pageIDLocked).
			d.idGone = append(d.idGone, gone.id)
			for len(d.idGone) > idKeep {
				if old := d.idGone[0]; !d.msgIDs[old] {
					delete(d.idOwner, old)
				}
				d.idGone = d.idGone[1:]
			}
		}
		d.msgs = slices.Delete(d.msgs, i, i+1)
		if mine {
			d.ownMsgs--
		}
	}
	if d.ownMsgs > maxPersonLog {
		drop(true)
	}
	if !own && len(d.msgs)-d.ownMsgs >= d.opt.PageMessages {
		drop(false)
	}
	d.msgSeq++
	id := d.pageIDLocked(e)
	d.msgs = append(d.msgs, pageMsg{d.msgSeq, e, id})
	if d.msgKeys == nil {
		d.msgKeys = map[string]bool{}
	}
	d.msgIDs[id], d.msgKeys[msgKey(e)] = true, true
}

// A cursor is this run of the daemon and the number of the last message returned. Numbers only go
// up, so a cursor still means "after this one" when the messages before it have been dropped. A
// cursor from another run, which a page could hold across a restart, has seen nothing. Nor has one
// from before earlier messages were read back from the logs, which numbers them all again
// (mergeMessagesLocked).
func (d *Daemon) cursor(seq uint64) string {
	return strconv.FormatInt(d.started.UnixNano(), 36) + "-" + strconv.FormatUint(seq, 10)
}

func (d *Daemon) cursorSeq(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	run, num, ok := strings.Cut(cursor, "-")
	seq, err := strconv.ParseUint(num, 10, 64)
	if !ok || err != nil {
		return 0, errors.New("bad cursor")
	}
	if run != strconv.FormatInt(d.started.UnixNano(), 36) {
		return 0, nil
	}
	return seq, nil
}

// Messages returns the messages after cursor, oldest first as this daemon read them, which is not
// by the senders' clocks. Once earlier ones are read back from the logs (history.go), all the page
// has are in the order of their senders' clocks, as what is read back has no order of arrival; the
// page sorts what it shows.
func (s pageSource) Messages(ctx context.Context, cursor string) ([]web.Message, string, error) {
	d := s.d
	after, err := d.cursorSeq(cursor)
	if err != nil {
		return nil, "", err
	}
	d.awaitHistory(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pageSeenLocked(ctx, time.Now())

	after = min(after, d.msgSeq)
	first := sort.Search(len(d.msgs), func(i int) bool { return d.msgs[i].seq > after })
	label := d.labelerLocked()
	out := make([]web.Message, 0, len(d.msgs)-first)
	for _, m := range d.msgs[first:] {
		msg := web.Message{ID: m.id, From: label(m.e.From), Subject: m.e.Subject, Body: m.e.Body, At: m.e.At,
			FromAddr: m.e.From, ToAddr: m.e.To, FromPerson: bus.IsPerson(m.e.From), ToPerson: bus.IsPerson(m.e.To)}
		for _, f := range m.e.Files {
			if d.refuses(&d.pageRecv, m.e) {
				break // Receive files is off (#765)
			}
			msg.Files = append(msg.Files, web.File{Name: f.Name, Size: f.Size})
		}
		if msg.FromPerson {
			// The name the message carries, or the one their device gives, if it is theirs.
			name, _ := d.personSenderLocked(m.e)
			msg.From = nameOr(name, m.e.From)
		}
		switch {
		case m.e.To == "*":
			msg.To, msg.ToKind = "*", web.ToAll
		case bus.IsGroup(m.e.To):
			msg.To, msg.ToKind = m.e.To, web.ToGroup
		case !strings.Contains(m.e.To, "/"):
			msg.To, msg.ToKind = m.e.To, web.ToDevice
		default:
			msg.To, msg.ToKind = label(m.e.To), web.ToSession
		}
		out = append(out, msg)
	}
	return out, d.cursor(d.msgSeq), nil
}

// pageSettings is what settings.json holds: the human's choices, and nothing the daemon works out.
type pageSettings struct {
	Wake        web.Wake            `json:"wake"`
	SessionWake map[string]web.Wake `json:"sessionWake,omitempty"`
	Busy        string              `json:"busy"`
	SessionBusy map[string]string   `json:"sessionBusy,omitempty"`
	Mode        string              `json:"mode,omitempty"`
	// SendFiles, ReceiveFiles and the agents' overrides are web.Settings's (#765): off unless
	// saved on.
	SendFiles        bool `json:"sendFiles,omitempty"`
	ReceiveFiles     bool `json:"receiveFiles,omitempty"`
	AgentsSendOff    bool `json:"agentsSendOff,omitempty"`
	AgentsReceiveOff bool `json:"agentsReceiveOff,omitempty"`
	// Away is when the device last left online for another mode, kept so that going back
	// releases what arrived since (mode.go), across restarts too.
	Away time.Time `json:"away,omitzero"`
}

// defaultSettings are what stands with no settings.json, and what one from before a setting
// existed has for it.
func defaultSettings() pageSettings {
	return pageSettings{Wake: web.Wake{When: web.WakeWithin}, Busy: web.BusySubject}
}

// clone is p with maps of its own, so it can be read without setMu.
func (p pageSettings) clone() pageSettings {
	p.SessionWake = maps.Clone(p.SessionWake)
	p.SessionBusy = maps.Clone(p.SessionBusy)
	return p
}

func (p pageSettings) view() web.Settings {
	return web.Settings{Wake: p.Wake, SessionWake: maps.Clone(p.SessionWake), Busy: p.Busy,
		SessionBusy: maps.Clone(p.SessionBusy), Mode: p.Mode, SendFiles: p.SendFiles, ReceiveFiles: p.ReceiveFiles,
		AgentsSendOff: p.AgentsSendOff, AgentsReceiveOff: p.AgentsReceiveOff,
		DefaultMinutes: defaultWakeMinutes}
}

func (d *Daemon) settingsPath() string { return filepath.Join(d.dir, "settings.json") }

// loadSettings reads settings.json, or takes the defaults if there is none or it holds something
// the page wouldn't have saved. The file is left alone then: the next save replaces it.
func (d *Daemon) loadSettings() {
	d.settings = defaultSettings()
	b, err := os.ReadFile(d.settingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	read := defaultSettings() // a setting the file doesn't have keeps its default
	if err == nil {
		if err = json.Unmarshal(b, &read); err == nil {
			err = web.CheckSettings(read.view())
		}
	}
	if err != nil {
		d.log.Printf("settings.json isn't usable, so the defaults stand: %v", err)
		return
	}
	d.settings = read
	d.hidden.Store(read.Mode == web.ModeInvisible)
	d.dnd.Store(read.Mode == web.ModeDND)
	d.applyFiles(read)
}

// applyFiles sets the file switches the daemon reads from p (#765, Tyler, 2026-11-16): sending,
// and receiving, is the person's when its main switch is on, and their agents' too unless kept
// from them. It says whether either side's receiving changed.
func (d *Daemon) applyFiles(p pageSettings) (agents, page bool) {
	d.agentsSendOff.Store(p.AgentsSendOff)
	d.agentsReceiveOff.Store(p.AgentsReceiveOff)
	d.pageSend.Store(p.SendFiles)
	d.agentSend.Store(p.SendFiles && !p.AgentsSendOff)
	return d.agentRecv.set(p.ReceiveFiles && !p.AgentsReceiveOff), d.pageRecv.set(p.ReceiveFiles)
}

func (s pageSource) Settings(ctx context.Context) (web.Settings, error) {
	s.d.pageCalled(ctx)
	s.d.setMu.Lock()
	defer s.d.setMu.Unlock()
	if s.d.settingsMove.Load() {
		s.d.moveSettings() // so the page reads each session's choice under the name it has now
	}
	return s.d.settings.view(), nil
}

// SetSettings keeps the settings in memory only once they are on disk, so a failed save leaves
// the ones that were in force.
func (s pageSource) SetSettings(ctx context.Context, set web.Settings) (web.Settings, error) {
	d := s.d
	d.pageCalled(ctx)
	if err := web.CheckSettings(set); err != nil {
		return web.Settings{}, err
	}
	next := pageSettings{Wake: set.Wake, SessionWake: maps.Clone(set.SessionWake),
		Busy: cmp.Or(set.Busy, web.BusySubject), SessionBusy: maps.Clone(set.SessionBusy), Mode: set.Mode,
		SendFiles: set.SendFiles, ReceiveFiles: set.ReceiveFiles, AgentsSendOff: set.AgentsSendOff, AgentsReceiveOff: set.AgentsReceiveOff}
	if next.Mode == web.ModeOnline {
		next.Mode = ""
	}
	if len(next.SessionWake) == 0 {
		next.SessionWake = nil
	}
	if len(next.SessionBusy) == 0 {
		next.SessionBusy = nil
	}
	d.setMu.Lock()
	defer d.setMu.Unlock()
	if d.settingsMove.Load() {
		d.moveSettings()
	}
	d.movedSettings(&next) // a page that read before a session's name changed (#836)
	// The file switches this save doesn't name stay as they are.
	for _, sw := range []struct {
		name      string
		next, cur *bool
	}{
		{"sendFiles", &next.SendFiles, &d.settings.SendFiles},
		{"receiveFiles", &next.ReceiveFiles, &d.settings.ReceiveFiles},
		{"agentsSendOff", &next.AgentsSendOff, &d.settings.AgentsSendOff},
		{"agentsReceiveOff", &next.AgentsReceiveOff, &d.settings.AgentsReceiveOff},
	} {
		if !slices.Contains(set.FilesSet, sw.name) {
			*sw.next = *sw.cur
		}
	}
	was := d.settings.Mode
	switch {
	case next.Mode == "":
	case was == "":
		next.Away = time.Now()
	default:
		next.Away = d.settings.Away // from one mode to the other: still away since then
	}
	if err := writeJSON(d.settingsPath(), next); err != nil {
		return web.Settings{}, fmt.Errorf("saving settings: %w", err)
	}
	away := d.settings.Away
	d.settings = next
	agents, page := d.applyFiles(next)
	if page && next.ReceiveFiles {
		d.mu.Lock()
		clear(d.fileMiss) // a fetch the switch's going off ended isn't one to wait out
		d.mu.Unlock()
	}
	if agents || page {
		d.kickPresence() // teammates' senders, who and lookup say whether this device takes files
	}
	if agents {
		d.kickFiles() // what waits for its files goes now, without them, if receiving went off
	}
	if next.Mode != was {
		d.setMode(was, next.Mode, away)
	}
	d.kickPush() // a session that was set to never may be woken now, for what it has waiting
	return next.view(), nil
}

func (d *Daemon) pageCalled(ctx context.Context) {
	d.mu.Lock()
	d.pageSeenLocked(ctx, time.Now())
	d.mu.Unlock()
}

// pageSeenLocked notes a call from the page, which keeps the daemon running only if someone can
// see the page: a tab left in the background goes on polling for as long as the browser runs.
// Caller holds mu.
func (d *Daemon) pageSeenLocked(ctx context.Context, now time.Time) {
	if web.InApp(ctx) {
		d.lastApp = now
	}
	if web.Watched(ctx) {
		if !d.pageOpenLocked(now) {
			d.kickPoll() // a page opens: its view should not be 30 s behind
		}
		d.lastPage = now
	}
}

// pageOpenLocked says a page someone can see has called within PageOpen of now. Caller holds mu.
func (d *Daemon) pageOpenLocked(now time.Time) bool {
	return !d.lastPage.IsZero() && now.Sub(d.lastPage) <= d.opt.PageOpen
}
