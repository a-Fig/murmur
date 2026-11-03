package daemon

import (
	"context"
	"crypto/ecdh"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// An encrypted repo (#156): once any device has written the config ref saying so, every device
// that reads it encrypts what it writes for the devices it is for, and never writes in the clear
// again. Messages go to their recipient's device and the sender's own; broadcasts, session
// events and presence to every device whose box key is known. A recipient whose key isn't known
// is refused a message rather than sent it in the clear. The name registry stays readable: it
// holds names, made at random, and the addresses they stand for, and every device needs them.

// loadBoxKey reads this clone's box key, creating it on first run. A new box key needs no new
// device ID, unlike a signing key: the device announces it in its sealed presence.
func loadBoxKey(dir string) (*ecdh.PrivateKey, error) {
	path := filepath.Join(dir, "box")
	if b, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil {
			if k, err := ecdh.X25519().NewPrivateKey(raw); err == nil {
				return k, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k := bus.NewBoxKey()
	return k, os.WriteFile(path, []byte(hex.EncodeToString(k.Bytes())+"\n"), 0o600)
}

// encryptEventLocked returns e as this device writes it: as it is, in a repo that isn't
// encrypted; else encrypted for the devices it is for. Caller holds mu.
func (d *Daemon) encryptEventLocked(e bus.Event) (bus.Event, error) {
	if !d.st.Encrypted {
		return e, nil
	}
	to := map[string]string{d.device: d.st.BoxKeys[d.device]}
	dev, _ := bus.SplitAddr(e.To)
	switch {
	case e.Type == bus.Message && e.To != "*":
		k := d.st.BoxKeys[dev]
		if k == "" {
			return bus.Event{}, fmt.Errorf("this repo's messages are encrypted, and %s's key isn't known yet: "+
				"its Murmur may be too old to encrypt, or it hasn't been seen since it started", dev)
		}
		to[dev] = k
	default:
		for dev, k := range d.st.BoxKeys {
			to[dev] = k
		}
	}
	return bus.EncryptEvent(e, to)
}

// encryptPresenceLocked does for a presence what encryptEventLocked does for an event, for every
// device whose box key is known. Caller holds mu.
func (d *Daemon) encryptPresenceLocked(p bus.Presence) (bus.Presence, error) {
	if !d.st.Encrypted {
		return p, nil
	}
	return bus.EncryptPresence(p, d.st.BoxKeys)
}

// readConfig reads the config ref. Only a record sealed by a device, and taken as its by
// sealOKLocked, counts; and only a repo turning encryption on, which nothing turns off. Caller
// holds advMu.
func (d *Daemon) readConfig(ctx context.Context, sha string) {
	d.mu.Lock()
	seen, done := d.configSHA == sha, d.st.Encrypted
	d.mu.Unlock()
	if seen || done {
		return
	}
	c, err := d.repo.ReadCommit(ctx, sha)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Printf("reading %s: %v", brand.ConfigRef, err)
		}
		return
	}
	var cfg bus.Config
	state, seal := bus.Open(c.Message, brand.ConfigRef, c.Parents)
	d.cutMu.Lock() // a write under way lands first; any after it is encrypted
	defer d.cutMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.configSHA = sha
	if bus.Decode(c.Message, &cfg) != nil || cfg.V != bus.Version || !cfg.Encrypted || state != bus.Sealed ||
		!d.sealOKLocked(seal.By, brand.ConfigRef, c) {
		return
	}
	d.log.Printf("%s turned encryption on for this repo; everything this device writes is encrypted from now on", seal.By)
	d.st.Encrypted = true
	d.saveLocked()
	d.kickPresence()
}

// encrypt turns encryption on for the repo, for OpEncrypt. Every device seen lately must be able to
// take part, unless force: one whose Murmur predates encryption would keep writing in the clear,
// and nothing here can stop it.
func (d *Daemon) encrypt(ctx context.Context, force bool) proto.Response {
	d.mu.Lock()
	if d.st.Encrypted {
		d.mu.Unlock()
		return proto.Response{OK: true, Note: "This repo's bus is already encrypted."}
	}
	var behind []string
	for _, id := range sortedKeys(d.peers) {
		// The device's latest presence, not the keys kept: one that rolled back to an older
		// Murmur stops publishing its key.
		if p := d.peers[id]; !d.staleLocked(p) && (d.unsealed[id] || p.pres.BoxKey == "" || d.st.BoxKeys[id] == "") {
			behind = append(behind, id)
		}
	}
	d.mu.Unlock()
	if len(behind) > 0 && !force {
		return proto.Response{Error: fmt.Sprintf("these devices run a Murmur too old to encrypt, and would go on "+
			"writing in the clear: %s. Update them first, or turn encryption on anyway with --force.",
			strings.Join(behind, ", "))}
	}
	cfg := bus.Config{V: bus.Version, Encrypted: true, At: time.Now().UTC()}
	sha, err := d.repo.NewRecord(ctx, brand.ConfigRef, "", cfg, d.device+" encrypts this repo's bus")
	if err == nil {
		err = d.push(ctx, sha, brand.ConfigRef, bus.Force, "")
	}
	if err != nil {
		return proto.Response{Error: "turning encryption on: " + err.Error()}
	}
	d.cutMu.Lock()
	d.mu.Lock()
	d.configSHA = sha
	d.st.Encrypted = true
	d.saveLocked()
	d.kickPresence()
	d.mu.Unlock()
	d.cutMu.Unlock()
	d.log.Printf("encryption turned on for this repo")
	note := "Encryption is on: from now on every device in this repo encrypts what it writes to the bus."
	if len(behind) > 0 {
		note += " These devices can't, and will go on writing in the clear until they are updated: " +
			strings.Join(behind, ", ") + "."
	}
	return proto.Response{OK: true, Note: note}
}
