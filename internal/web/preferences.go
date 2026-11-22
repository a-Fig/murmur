package web

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

const PreferencesFile = "page-preferences.json"

// Preferences holds the page choices shared by the browser and the desktop app.
// An absent field can still be filled from a page's old localStorage.
type Preferences struct {
	Theme      string `json:"theme,omitempty"`
	OnlyMine   *bool  `json:"onlyMine,omitempty"`
	TagsFolded *bool  `json:"tagsFolded,omitempty"`
}

func (p Preferences) theme() string { return cmp.Or(p.Theme, "dim") }

// DarkFrame is the color scheme the desktop window uses before the page loads.
func (p Preferences) DarkFrame() bool {
	return p.Theme != "light" && p.Theme != "murmur" && p.Theme != "sky"
}

func (p Preferences) valid() bool {
	switch p.Theme {
	case "", "dark", "dim", "light", "murmur", "sky":
		return true
	}
	return false
}

// ReadPreferences is used by the daemon and by the app before it makes its window.
func ReadPreferences(dir string) (Preferences, error) {
	var p Preferences
	b, err := os.ReadFile(filepath.Join(dir, PreferencesFile))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		// The next successful change replaces the unreadable file with valid preferences.
		return Preferences{}, err
	}
	if !p.valid() {
		// Keep the other choices if a newer version saved a theme this build lacks.
		p.Theme = ""
	}
	return p, nil
}

// WithPreferences sets the initial preferences and their disk writer before serving the page.
// Without a writer, as on the devserver, changes last for the lifetime of the server.
func (s *Server) WithPreferences(p Preferences, save func(Preferences) error) {
	s.preferences, s.savePreferences = p, save
}

func (s *Server) currentPreferences() Preferences {
	s.prefMu.Lock()
	defer s.prefMu.Unlock()
	return s.preferences
}

func (s *Server) getPreferences(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.currentPreferences())
}

// PATCH changes only the named choices. POST imports old local choices only where the daemon
// has none, so opening an older browser profile cannot undo a choice made in the app.
func (s *Server) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var p Preferences
	if err := readJSON(w, r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !p.valid() {
		writeError(w, http.StatusBadRequest, "unknown theme")
		return
	}
	s.prefMu.Lock()
	defer s.prefMu.Unlock()
	was := s.preferences
	next := Preferences{cmp.Or(p.Theme, was.Theme), cmp.Or(p.OnlyMine, was.OnlyMine), cmp.Or(p.TagsFolded, was.TagsFolded)}
	if r.Method == http.MethodPost {
		next = Preferences{cmp.Or(was.Theme, p.Theme), cmp.Or(was.OnlyMine, p.OnlyMine), cmp.Or(was.TagsFolded, p.TagsFolded)}
	}
	if next != was && s.savePreferences != nil {
		if err := s.savePreferences(next); err != nil {
			writeError(w, http.StatusInternalServerError, "couldn't save page preferences")
			return
		}
	}
	s.preferences = next
	writeJSON(w, next)
}
