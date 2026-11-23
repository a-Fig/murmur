package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Switching repos (#348). Each clone's daemon serves its own page, on its own port with its own
// token, so the page's origin is one clone's. Moving the tab to another clone's page moves it to
// another origin, which needs that page's token, and the token mustn't travel in an address: an
// address reaches the browser's history and its extensions.
//
// So the page asks its own daemon to open the other clone (Repos.Open). That daemon starts the
// other's if it isn't running and asks it, over its sessions' port with its sessions' token, for a
// key (PageKey): random, good once, for a minute. The page goes to the other page's address with
// the key in the fragment, and that page trades the key for its token at /api/key, which answers
// only a request that carries JSON, so another site can't send one without a preflight that's
// never granted. The token goes from the daemon that holds it to its own page and nowhere else, and
// the key in the tab's history is spent.

// Repos is this machine's clones that joined, as the page's repo picker shows them. Nil: the
// page shows its own repo and no others.
type Repos interface {
	// List is the clones, this one among them.
	List(ctx context.Context) ([]Repo, error)
	// Open starts the clone's daemon if it isn't running and returns its page's address with a
	// one-time key in the fragment, #key=..., which that page trades for its token.
	Open(ctx context.Context, id string) (string, error)
}

// Repo is one clone on this machine.
type Repo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug,omitempty"`    // owner/name when its remote's address has an owner, else Name; "" when not known
	Path    string `json:"path"`              // its top directory
	Running bool   `json:"running,omitempty"` // its daemon answers
	Current bool   `json:"current,omitempty"` // the clone this page is
}

// ErrNoRepo is Open's answer for a clone that isn't on the list, or is no longer there.
var ErrNoRepo = errors.New("that repo isn't joined on this machine any more")

const (
	keyLife = time.Minute
	maxKeys = 16
	// openTimeout covers starting a daemon, whose first sync of the remote may take 10 s, and the
	// replacement of one left by an older version, which may take 40.
	openTimeout = 90 * time.Second
)

type keyring struct {
	mu   sync.Mutex
	keys map[string]time.Time // key → when it stops working
}

// PageKey makes a key that this page's server trades once, within a minute, for the page's token.
// The daemon hands it only to another clone's daemon, which asked with the sessions' token.
func (s *Server) PageKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	key := hex.EncodeToString(b)
	k := &s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if k.keys == nil {
		k.keys = map[string]time.Time{}
	}
	for old, end := range k.keys {
		if now.After(end) {
			delete(k.keys, old)
		}
	}
	for len(k.keys) >= maxKeys { // the soonest to expire goes
		var first string
		for old, end := range k.keys {
			if first == "" || end.Before(k.keys[first]) {
				first = old
			}
		}
		delete(k.keys, first)
	}
	k.keys[key] = now.Add(keyLife)
	return key
}

// KeysOut says a key is out that is still good: a tab may be on its way to trade it. A daemon
// restarting onto an update waits for none to be, since the next daemon's page doesn't know them.
func (s *Server) KeysOut() bool {
	k := &s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	for key, end := range k.keys {
		if now.After(end) {
			delete(k.keys, key)
		}
	}
	return len(k.keys) > 0
}

// spend reports whether key is a live key, and spends it.
func (s *Server) spend(key string) bool {
	k := &s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	ok := false
	for live, end := range k.keys {
		// Every key is compared, so the time taken says nothing of how near a guess came.
		if subtle.ConstantTimeCompare([]byte(live), []byte(key)) == 1 && !now.After(end) {
			ok = true
			delete(k.keys, live)
		}
	}
	return ok
}

// WithRepos lets the page list this machine's clones and switch to them. Call it before serving.
func (s *Server) WithRepos(r Repos) *Server {
	s.repos = r
	return s
}

type keyRequest struct {
	Key string `json:"key"`
}

type keyResponse struct {
	Token string `json:"token"`
}

// postKey trades a key for the page's token. It is the one API call without the token, since the
// token is what it gives. It takes JSON only: a form or plain text could be sent from any site
// without asking, and JSON can't be sent across sites without a preflight, which nothing here
// answers.
func (s *Server) postKey(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "a key comes as JSON")
		return
	}
	var req keyRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Key == "" || len(req.Key) > 128 || s.token == "" || !s.spend(req.Key) {
		writeError(w, http.StatusUnauthorized, "that key is spent, out of date or not this page's")
		return
	}
	writeJSON(w, keyResponse{Token: s.token})
}

type reposResponse struct {
	Repos []Repo `json:"repos"`
}

func (s *Server) getRepos(w http.ResponseWriter, r *http.Request) {
	if s.repos == nil {
		writeJSON(w, reposResponse{Repos: []Repo{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	list, err := s.repos.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []Repo{}
	}
	writeJSON(w, reposResponse{Repos: list})
}

type openRequest struct {
	ID string `json:"id"`
}

type openResponse struct {
	URL string `json:"url"`
}

func (s *Server) postOpenRepo(w http.ResponseWriter, r *http.Request) {
	var req openRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.repos == nil || req.ID == "" || len(req.ID) > maxNameLen {
		writeError(w, http.StatusNotFound, ErrNoRepo.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), openTimeout)
	defer cancel()
	u, err := s.repos.Open(ctx, req.ID)
	if errors.Is(err, ErrNoRepo) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !keyURL(u) {
		writeError(w, http.StatusBadGateway, "that repo's daemon gave an address that isn't a page on 127.0.0.1")
		return
	}
	writeJSON(w, openResponse{URL: u})
}

// keyURL says u is a page's address on 127.0.0.1 with a key and nothing else in its fragment:
// where the tab may be sent.
func keyURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "http" || p.Path != "/" || p.RawQuery != "" || p.User != nil {
		return false
	}
	host, port, err := net.SplitHostPort(p.Host)
	if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
		return false
	}
	key, ok := strings.CutPrefix(p.Fragment, "key=")
	return ok && key != "" && !strings.ContainsAny(key, "&=#")
}
