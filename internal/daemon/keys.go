package daemon

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proc"
)

// The bus is encrypted (#156): every record with content goes out encrypted for the team key
// (team.go), with nothing for anyone to run, approve or copy. Each device makes its own keys at
// start, publishes the public halves in its sealed presence, and pins its teammates' keys the first
// time it reads them. Push access is the key directory: only collaborators can write refs/murmur/*,
// so only they can publish a key, and only they are granted the team key. docs/design/encryption.md
// has the reasons and what it costs.

// keyPin is what this device knows of a device's keys: the signing key it pinned on first sight,
// the box key that device's latest sealed presence carried, the latest time a sealed presence of it
// was signed with (At, a high-water mark: see newPresenceLocked), and when this daemon last took a
// new one from it, by this machine's clock (Seen, see noteSeenLocked).
type keyPin struct {
	Sign string    `json:"sign"`
	Box  string    `json:"box"`
	At   time.Time `json:"at,omitzero"`
	Seen time.Time `json:"seen,omitzero"`
}

// keyFile is the device's private keys, in .git/murmur/keys: never committed, never on the bus.
type keyFile struct {
	Sign string `json:"sign"` // Ed25519 seed, hex
	Box  string `json:"box"`  // X25519 private key, hex
}

// InitialTeamRecord makes the first team epoch for the clone running init. Keeping its private
// half in the signed record, wrapped for this clone, lets the daemon recover it even if init
// stops just after pushing. Use the daemon lock so two worktrees can't create different keys.
func InitialTeamRecord(ctx context.Context, gitDir string) (string, error) {
	dir := filepath.Join(gitDir, brand.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	lock, err := proc.Lock(filepath.Join(dir, "daemon.lock"))
	if errors.Is(err, proc.ErrLocked) {
		return "", errors.New("stop this clone's Murmur daemon before initializing its mesh")
	}
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := proc.Protect(dir); err != nil {
		return "", err
	}
	device, sign, box, err := loadIdentity(dir, nil, sealedLocally(ctx, gitx.Git{Dir: gitDir}))
	if err != nil {
		return "", err
	}
	record, err := bus.NewEpoch(bus.NewBoxKey(), map[string]string{device: bus.BoxPublic(box)}, time.Now())
	if err != nil {
		return "", err
	}
	record.AdminSeq = 1
	record.Policy = &bus.RepoPolicy{EncryptionMode: "team", AllowFiles: true}
	return bus.EncodeSealed(record, record.Subject(), sign, device, brand.TeamRef)
}

// loadIdentity reads this clone's device ID and keys, creating what is missing. The keys are kept
// next to the device ID, in a file of their own, so a daemon from before encryption, which reads
// only the device file, still finds its ID.
//
// A device that has published keys is known to its teammates by them, and they refuse any other
// under its ID. So a device whose keys are lost takes a new ID with new keys: to its teammates it is
// a new device, rather than one whose presence they refuse. It had keys if its state pinned them,
// or, should state.json be gone too, if the clone's copy of its own presence is sealed (sealed, as
// git reads it from that copy). A device that never had keys, a daemon from before encryption,
// keeps its ID.
func loadIdentity(dir string, lg *log.Logger, sealed func(device string) bool) (device string, sign ed25519.PrivateKey, box *ecdh.PrivateKey, err error) {
	device, err = loadDevice(dir)
	if err != nil {
		return "", nil, nil, err
	}
	path := filepath.Join(dir, "keys")
	b, err := os.ReadFile(path)
	if err == nil {
		if sign, box, ok := parseKeys(b); ok {
			return device, sign, box, nil
		}
		if lg != nil {
			lg.Printf("%s is unreadable; making new keys", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, nil, err
	}
	if hadKeys(filepath.Join(dir, "state.json"), device) || sealed != nil && sealed(device) {
		// The names go before the ID does: a start that fails after the new ID and keys are written
		// would leave the next one nothing to tell it the ID changed, and it would keep the names.
		if err := dropOldNames(filepath.Join(dir, "state.json")); err != nil {
			return "", nil, nil, err
		}
		if err := os.Remove(filepath.Join(dir, "device")); err != nil {
			return "", nil, nil, err
		}
		old := device
		if device, err = loadDevice(dir); err != nil {
			return "", nil, nil, err
		}
		if lg != nil {
			lg.Printf("the keys of %s are gone; this clone is device %s from now on", old, device)
		}
	}
	sign, box = bus.NewSignKey(), bus.NewBoxKey()
	out, err := json.Marshal(keyFile{Sign: hex.EncodeToString(sign.Seed()), Box: hex.EncodeToString(box.Bytes())})
	if err != nil {
		return "", nil, nil, err
	}
	if err := writeFileAtomic(path, append(out, '\n')); err != nil {
		return "", nil, nil, err
	}
	return device, sign, box, nil
}

// dropOldNames takes out of the state file at path the session names this clone holds, before
// loadIdentity gives it a new device ID. The registry has each of them on the old device, or set
// aside for it, and a new device can't take them over: a move between devices is something the
// registry never allows. Kept, they were what a reconnecting session took under the new ID, and
// names the old one had set aside went to new sessions, while every other device still had them on
// the old one: a message to one, once its session stopped, went to the old address, where nobody
// would read it (#617). So the new ID starts with new names, as on any other new device, and the
// old ones stay taken (Used).
//
// What a session kept by its name is the session's, not the name's: its tags, with the groups it
// is in and do not disturb, its mutes, and who has been explained to it. So which name each key
// had goes to namesLeft, for the names that have any of those, are muted by another session
// here, or have a wake or busy choice of their own on the page (#836), and the name the key gets next takes them (adoptLeftLocked, #827). Without that a session
// in a group, on do not disturb, came back out of the group and was given turns again.
//
// The starts and stops not yet written (unwritten) go too: they are from the old device's
// addresses, and written to the new device's log every reader would drop them, their sender not
// being the log's device. Everything else in the file is kept as it was.
func dropOldNames(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st map[string]json.RawMessage
	if json.Unmarshal(b, &st) != nil {
		return nil // loadState says what is wrong with it
	}
	n := len(st)
	kept := map[string]bool{} // the names that have something to hand on
	for _, k := range []string{"tags", "mutes", "explained"} {
		var by map[string]json.RawMessage
		if json.Unmarshal(st[k], &by) == nil {
			for name := range by {
				kept[name] = true
			}
		}
	}
	// And the names muted: a session another one here muted is still muted under its new name.
	var mutes map[string]map[string]json.RawMessage
	if json.Unmarshal(st["mutes"], &mutes) == nil {
		for _, m := range mutes {
			for name := range m {
				kept[name] = true
			}
		}
	}
	// And the names the page has a wake or busy choice for (settings.json, #836).
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "settings.json")); err == nil {
		var set struct {
			SessionWake map[string]json.RawMessage `json:"sessionWake"`
			SessionBusy map[string]json.RawMessage `json:"sessionBusy"`
		}
		if json.Unmarshal(b, &set) == nil {
			for _, by := range []map[string]json.RawMessage{set.SessionWake, set.SessionBusy} {
				for name := range by {
					kept[name] = true
				}
			}
		}
	}
	// A choice still to be moved to a name (settingsMoves) is that name's.
	var moves [][2]string
	if json.Unmarshal(st["settingsMoves"], &moves) == nil {
		for _, m := range moves {
			if kept[m[0]] {
				kept[m[1]] = true
			}
		}
	}
	// An earlier loss's are kept for the keys that haven't come back since, and for one whose
	// name since has nothing: its key was given a name with no session there to take them.
	var earlier, keys map[string]string
	_ = json.Unmarshal(st["namesLeft"], &earlier)
	_ = json.Unmarshal(st["nameKeys"], &keys)
	left := map[string]string{}
	for _, names := range []map[string]string{earlier, keys} {
		for key, name := range names {
			if key != "" && kept[name] {
				left[key] = name
			}
		}
	}
	was := string(st["namesLeft"])
	for _, k := range []string{"nameKeys", "namePool", "nameBinds", "nameHolders", "sessionNames", "namesSeen", "unwritten", "namesLeft"} {
		delete(st, k)
	}
	if len(left) > 0 {
		b, err := json.Marshal(left)
		if err != nil {
			return err
		}
		st["namesLeft"] = b
	}
	if len(st) == n && string(st["namesLeft"]) == was {
		return nil
	}
	// Written as the daemon writes it, and with the time it had: loadState takes that for when the
	// last daemon last saved, which dates the stops of the sessions a killed one left (Opus's review).
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := writeJSON(path, st); err != nil {
		return err
	}
	return os.Chtimes(path, fi.ModTime(), fi.ModTime())
}

func parseKeys(b []byte) (ed25519.PrivateKey, *ecdh.PrivateKey, bool) {
	var kf keyFile
	if json.Unmarshal(b, &kf) != nil {
		return nil, nil, false
	}
	seed, err := hex.DecodeString(kf.Sign)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, nil, false
	}
	raw, err := hex.DecodeString(kf.Box)
	if err != nil {
		return nil, nil, false
	}
	box, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, nil, false
	}
	return ed25519.NewKeyFromSeed(seed), box, true
}

// writeFileAtomic writes a private file whole or not at all.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// hadKeys says whether the state file records keys for device, which it does once the device has
// run with them.
func hadKeys(path, device string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var st struct {
		Pins map[string]*keyPin `json:"pins"`
	}
	return json.Unmarshal(b, &st) == nil && st.Pins[device] != nil
}

// sealedLocally says whether the clone's local copy of device's presence ref is sealed by device:
// it published keys. A ref that isn't there, or can't be read, says no.
func sealedLocally(ctx context.Context, g gitx.Git) func(string) bool {
	return func(device string) bool {
		ref := brand.PresencePrefix + device
		out, err := g.Run(ctx, "", "log", "-1", "--no-show-signature", "--no-color", "--format=%B", ref, "--")
		if err != nil {
			return false
		}
		st, seal := bus.OpenSeal(out, ref)
		return st == bus.Sealed && seal.By == device
	}
}

// pinSelfLocked records this device's own keys among the pins, which is what tells a later start
// that keys gone missing were this device's (see loadIdentity). Caller holds mu.
func (d *Daemon) pinSelfLocked() {
	pin := &keyPin{Sign: bus.SignPublic(d.signKey), Box: bus.BoxPublic(d.boxKey), Seen: time.Now().UTC()}
	if old := d.st.Pins[d.device]; old == nil || old.Sign != pin.Sign || old.Box != pin.Box {
		d.st.Pins[d.device] = pin
		d.saveLocked()
	}
}

// checkPresenceLocked decides whether a presence read from dev's ref is to be taken as dev's, and
// pins dev's keys from it. The first sealed presence read for a device pins its signing key, and
// from then on only presence sealed with that key is: none unsealed, so a seal can't be stripped
// off, and none with another key, so a pin can't be undone by the next writer. Until then an
// unsealed presence is taken as it always was, since daemons from before encryption write nothing
// else. A refusal is logged, at most once a minute for each device, and nobody is asked anything.
//
// This trusts whoever seals first. Anyone who can push could seal a presence for a device before a
// reader has seen the device's own, and that reader would then refuse the device: what a team of
// people who can all push already trust each other with. Caller holds mu.
func (d *Daemon) checkPresenceLocked(dev, ref string, c bus.Commit, pres bus.Presence) (ok, sealed bool) {
	state, seal := bus.OpenSeal(c.Message, ref)
	pin := d.st.Pins[dev]
	switch {
	case state == bus.Unsealed && pin == nil && pres.BoxKey == "" && pres.Box == nil:
		return true, false // a daemon from before encryption
	case state == bus.Sealed && seal.By == dev && (pin == nil || pin.Sign == seal.Key) && bus.ValidBoxKey(pres.BoxKey):
		switch {
		case pin == nil:
			d.st.Pins[dev] = &keyPin{Sign: seal.Key, Box: pres.BoxKey}
			d.saveLocked()
		case pin.Box != pres.BoxKey:
			pin.Box = pres.BoxKey
			d.saveLocked()
		}
		return true, true
	}
	why := "its seal doesn't hold"
	switch {
	case state == bus.Unsealed && pin != nil:
		why = "it has no seal, and " + dev + " seals its presence"
	case state == bus.Unsealed:
		why = "it carries a key but no seal"
	case state == bus.Sealed && seal.By != dev:
		why = "it is sealed by " + seal.By
	case state == bus.Sealed && pin != nil && pin.Sign != seal.Key:
		why = "it is sealed with a key that isn't the one pinned for " + dev
	case state == bus.Sealed:
		why = "its box key isn't a usable key"
	}
	if t, ok := d.refused[dev]; !ok || time.Since(t) > time.Minute {
		d.log.Printf("%s: ignoring %s, which claims to be from %s: %s", ref, short(c.SHA), dev, why)
	}
	d.refused[dev] = time.Now()
	return false, false
}

// newPresenceLocked says whether a sealed presence of dev's, signed with time at, is one dev wrote
// after every other this daemon has taken from it, and raises the high-water mark if so. Anyone who
// can push could put an older presence of a device back on its ref, or wrap the same one in a new
// commit, and a device whose user has lost push access would then look current for good (Sol's
// review of #156); neither is new by this. The time is the one inside the seal, so a new commit
// can't change it, and a device's own presence times never go back (nextPresenceAtLocked). The mark
// is kept in state.json, as a restart mustn't take an old presence for a new one. Caller holds mu.
func (d *Daemon) newPresenceLocked(dev string, at time.Time) bool {
	pin := d.st.Pins[dev]
	if pin == nil || !at.After(pin.At) {
		return false
	}
	pin.At = at.UTC()
	d.saveLocked()
	return true
}

// nextPresenceAtLocked is the time this device signs its next presence with: now, or just after the
// last, if the clock has gone back since, so its teammates take every new presence as new
// (newPresenceLocked). The last is kept in state.json. Caller holds mu.
func (d *Daemon) nextPresenceAtLocked() time.Time {
	at := time.Now().UTC().Round(0)
	if !at.After(d.st.PresenceAt) {
		at = d.st.PresenceAt.Add(time.Millisecond)
	}
	d.st.PresenceAt = at
	d.saveLocked()
	return at
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// noKeyError is a message refused because its recipient's device has no key this one can encrypt
// to. It is about that recipient alone, so a send to a list goes on past it (#541).
type noKeyError struct{ error }

// errOldDevice says a device can't read what this one writes.
func errOldDevice(dev string) error {
	return noKeyError{fmt.Errorf("%s runs a version of Murmur from before the bus was encrypted, so it couldn't read this. "+
		"It can once it runs Murmur 0.3 or later. Versions after 0.2.3 check for a newer release on their channel every hour, "+
		"unless updates are turned off there; 0.2.3 and older don't, so that teammate runs Murmur's installer again "+
		"(https://murmur.afig.dev)", dev)}
}

// encryptEventLocked encodes e under the verified repo policy. Private messages and receipts
// use their recipients' device keys. Other encrypted events use the team key. Both include the
// writer's device so it can read its own log before receiving a team grant. Caller holds mu.
func (d *Daemon) encryptEventLocked(e bus.Event) (bus.Event, error) {
	if err := d.checkOwnerHistoryLocked(); err != nil {
		return bus.Event{}, err
	}
	switch d.repoModeLocked() {
	case bus.EncryptionOff:
		return e, nil
	case bus.EncryptionPrivate:
		if e.Type == bus.Receipt && !bus.ValidDevice(e.To) {
			return bus.Event{}, errRepoModeChanged
		}
		if e.Type == bus.Message || e.Type == bus.GroupAdd || e.Type == bus.Receipt {
			to, err := d.privateRecipientsLocked(e.To)
			if err != nil {
				return bus.Event{}, err
			}
			return bus.EncryptEvent(e, d.device, to)
		}
	}
	if e.Type == bus.Message && e.To != "" && e.To != "*" && !bus.IsGroup(e.To) {
		dev, _ := bus.SplitAddr(e.To)
		if pin := d.st.Pins[dev]; dev != d.device && (pin == nil || pin.Box == "") {
			if p := d.peers[dev]; p != nil {
				return bus.Event{}, errOldDevice(dev)
			}
			return bus.Event{}, noKeyError{fmt.Errorf("%s isn't a device this one has seen, so there is no key to encrypt to", dev)}
		}
	}
	if d.epochLocked() == nil {
		return bus.Event{}, errNoTeamKey
	}
	to := d.teamRecipientsLocked()
	to[d.device] = bus.BoxPublic(d.boxKey)
	return bus.EncryptEvent(e, d.device, to)
}

// unknownKey is a record encrypted for a team key the team ref didn't show, and the sync it was
// first met in.
type unknownKey struct {
	sha string
	seq int64
}

// openEventLocked decrypts an event read from dev's log, if it is encrypted. ok is false for one
// this device can't read. wait says it is encrypted for a team key that is on the team ref but not
// yet granted to this device: the log is read no further until it is, so nothing is missed.
//
// A box for a team key the team ref doesn't show waits too, until a sync that began after this
// record was first met has read the team ref whole. A rotation's first records can be fetched before
// its epoch is read: the team ref's fetch failed in that sync, or the log's fetch went past what the
// sync listed. Passing over them at once would lose them for good (Sol's review, 2026-11-10). If
// that later sync still shows no such key, the box is junk, and passed over, so junk holds a log up
// for one sync and no longer. Caller holds mu.
func (d *Daemon) openEventLocked(e bus.Event, dev, ref, sha string) (out bus.Event, ok, wait bool) {
	if e.Type != bus.Boxed {
		return e, true, false
	}
	in, err := bus.DecryptEvent(e, dev, d.keyringLocked())
	if err == nil {
		return in, true, false
	}
	if !errors.Is(err, bus.ErrNotForUs) || e.Box == nil {
		d.log.Printf("%s: ignoring %s: %v", ref, short(sha), err)
		return bus.Event{}, false, false
	}
	if d.repoModeLocked() == bus.EncryptionOff && d.checkOwnerHistoryLocked() == nil {
		// Unreadable encrypted history must not hold newer plaintext events behind a grant.
		return bus.Event{}, false, false
	}
	to := e.Box.Recipients()
	if slices.ContainsFunc(to, d.knownEpochLocked) {
		delete(d.unknownAt, ref)
		return bus.Event{}, false, true
	}
	if !slices.ContainsFunc(to, bus.IsTeamID) {
		return bus.Event{}, false, false // for devices alone, and not this one
	}
	first, met := d.unknownAt[ref]
	if !met || first.sha != sha {
		first = unknownKey{sha: sha, seq: d.syncSeq}
		d.unknownAt[ref] = first
	}
	if d.teamReadSeq > first.seq {
		delete(d.unknownAt, ref)
		return bus.Event{}, false, false
	}
	return bus.Event{}, false, true
}
