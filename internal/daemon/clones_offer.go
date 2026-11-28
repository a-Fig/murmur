package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/web"
)

const (
	maxAccountOffers = 5
	offerFreshWindow = 2 * time.Second
	cloneReadTries   = 5
	cloneReadWait    = 20 * time.Millisecond
)

type cloneOfferCandidate struct {
	gitDir string
	repo   string
	name   string
	nick   string
	active time.Time
}

type offerCacheEntry struct {
	modTime time.Time
	size    int64
	slug    string
	ok      bool
	cand    cloneOfferCandidate
}

type peerCloneState struct {
	Person       string            `json:"person"`
	Nick         string            `json:"nick"`
	LastSync     time.Time         `json:"lastSync"`
	PersonSince  time.Time         `json:"personSince"`
	PersonClaims map[string]string `json:"personClaims"`
}

// cloneOfferCandidates reads the user's other joined clones on this machine (#1277) outside mu
// and returns distinct (person, nick) choices, ordered most recently active first. It returns nil
// immediately without disk I/O once this clone has a person name.
func (d *Daemon) cloneOfferCandidates(ctx context.Context) []cloneOfferCandidate {
	if d.hasPerson.Load() || d.opt.Clones == "" {
		return nil
	}
	joined := d.opt.Joined
	if joined == nil {
		joined = func(string) bool { return true }
	}
	list, err := clones.List(d.opt.Clones, joined)
	if err != nil || len(list) == 0 {
		return nil
	}
	selfID := clones.ID(d.opt.GitDir)
	seenDirs := make(map[string]bool, len(list))
	var cands []cloneOfferCandidate
	for _, c := range list {
		if clones.ID(c.GitDir) == selfID {
			continue
		}
		seenDirs[c.GitDir] = true
		if cand, ok := d.readCloneOfferCandidate(ctx, c); ok {
			cands = append(cands, cand)
		}
	}
	d.pruneOfferCache(seenDirs)
	if len(cands) == 0 {
		return nil
	}
	slices.SortFunc(cands, func(a, b cloneOfferCandidate) int {
		if d := b.active.Compare(a.active); d != 0 {
			return d
		}
		if d := cmp.Compare(a.repo, b.repo); d != 0 {
			return d
		}
		return cmp.Compare(a.gitDir, b.gitDir)
	})
	type pairKey struct{ name, nick string }
	seen := make(map[pairKey]bool, len(cands))
	out := make([]cloneOfferCandidate, 0, min(len(cands), maxAccountOffers))
	for _, c := range cands {
		k := pairKey{c.name, c.nick}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
		if len(out) >= maxAccountOffers {
			break
		}
	}
	return out
}

func (d *Daemon) readCloneOfferCandidate(ctx context.Context, c clones.Clone) (cloneOfferCandidate, bool) {
	stateDir := filepath.Join(c.GitDir, brand.StateDir)
	statePath := filepath.Join(stateDir, "state.json")
	info, err := os.Stat(statePath)
	if err != nil {
		return cloneOfferCandidate{}, false
	}

	d.offerMu.Lock()
	cached, haveCache := d.offerCache[c.GitDir]
	d.offerMu.Unlock()
	if haveCache && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() && time.Since(info.ModTime()) > offerFreshWindow {
		if !cached.ok {
			return cloneOfferCandidate{}, false
		}
		cand := cached.cand
		cand.repo = cmp.Or(c.Slug, cached.slug, c.Label())
		return cand, true
	}

	b, err := readCloneFile(statePath)
	if err != nil {
		return cloneOfferCandidate{}, false
	}
	var ps peerCloneState
	if json.Unmarshal(b, &ps) != nil {
		// Don't negative-cache unreadable JSON in case a peer daemon is mid-write.
		return cloneOfferCandidate{}, false
	}
	person := strings.TrimSpace(ps.Person)
	if person == "" || !bus.ValidPerson(person) {
		d.storeOfferCache(c.GitDir, offerCacheEntry{modTime: info.ModTime(), size: info.Size(), slug: cmp.Or(c.Slug, cached.slug)}, info.ModTime())
		return cloneOfferCandidate{}, false
	}
	if db, err := readCloneFile(filepath.Join(stateDir, "device")); err == nil {
		if peerDev := strings.TrimSpace(string(db)); peerDev != "" {
			if claim := ps.PersonClaims[person]; claim != "" && claim != bus.Addr(peerDev, bus.PersonSession) {
				// Another device holds this person name in that repo; don't offer a stale claim.
				d.storeOfferCache(c.GitDir, offerCacheEntry{modTime: info.ModTime(), size: info.Size(), slug: cmp.Or(c.Slug, cached.slug)}, info.ModTime())
				return cloneOfferCandidate{}, false
			}
		}
	}
	nick := strings.TrimSpace(ps.Nick)
	if nick != "" && !bus.ValidNick(nick) {
		nick = ""
	}
	slug := cmp.Or(c.Slug, cached.slug)
	if slug == "" {
		sCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		slug = clones.SlugIn(sCtx, c.GitDir)
		cancel()
	}
	active := info.ModTime()
	if ps.LastSync.After(active) {
		active = ps.LastSync
	}
	if ps.PersonSince.After(active) {
		active = ps.PersonSince
	}
	cand := cloneOfferCandidate{
		gitDir: c.GitDir,
		repo:   cmp.Or(slug, c.Label()),
		name:   person,
		nick:   nick,
		active: active,
	}
	d.storeOfferCache(c.GitDir, offerCacheEntry{
		modTime: info.ModTime(),
		size:    info.Size(),
		slug:    slug,
		ok:      true,
		cand:    cand,
	}, info.ModTime())
	return cand, true
}

func (d *Daemon) storeOfferCache(gitDir string, entry offerCacheEntry, modTime time.Time) {
	if time.Since(modTime) <= offerFreshWindow {
		return // skip caching files touched just now so rapid writes stay visible
	}
	d.offerMu.Lock()
	if d.offerCache == nil {
		d.offerCache = map[string]offerCacheEntry{}
	}
	d.offerCache[gitDir] = entry
	d.offerMu.Unlock()
}

func (d *Daemon) pruneOfferCache(seen map[string]bool) {
	d.offerMu.Lock()
	for k := range d.offerCache {
		if !seen[k] {
			delete(d.offerCache, k)
		}
	}
	d.offerMu.Unlock()
}

func readCloneFile(path string) ([]byte, error) {
	var err error
	for i := range cloneReadTries {
		var b []byte
		b, err = os.ReadFile(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return b, err
		}
		if i < cloneReadTries-1 {
			time.Sleep(cloneReadWait)
		}
	}
	return nil, err
}

func (d *Daemon) accountWithOffers(ctx context.Context) web.Account {
	cands := d.cloneOfferCandidates(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.accountWithOffersLocked(cands)
}

// accountWithOffersLocked is accountLocked plus any local cross-repo offers (#1277) when this
// clone's person hasn't chosen a name yet. Caller holds mu.
func (d *Daemon) accountWithOffersLocked(cands []cloneOfferCandidate) web.Account {
	a := d.accountLocked()
	if d.st.Person != "" || len(cands) == 0 {
		return a
	}
	a.Offers = make([]web.AccountOffer, 0, len(cands))
	for _, c := range cands {
		off := web.AccountOffer{
			Name: c.name,
			Nick: c.nick,
			Repo: c.repo,
		}
		if dev := d.nameTakenLocked(c.name); dev != "" {
			off.Taken = dev
		} else if why := d.personNameProblemLocked(c.name); why != "" {
			off.Problem = why
		}
		if off.Nick != "" {
			if why := d.nickProblemLocked(off.Nick); why != "" {
				off.NickProblem = why
				off.Nick = ""
			}
		}
		a.Offers = append(a.Offers, off)
	}
	return a
}
