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
func DarkFrame(theme string) bool {
	return theme != "light" && theme != "murmur" && theme != "sky" && theme != "day"
}

func validTheme(theme string) bool {
	switch theme {
	case "", "dark", "dim", "light", "murmur", "sky", "night", "day":
		return true
	}
	return false
}

// ReadPreferences reads the clone's filters and any theme saved by an older build.
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
	if !validTheme(p.Theme) {
		// Keep the other choices if a newer version saved a theme this build lacks.
		p.Theme = ""
	}
	return p, nil
}

// WithPreferences sets the initial preferences and their disk writer before serving the page.
// Without a writer, as on the devserver, changes last for the lifetime of the server.
func (s *Server) WithPreferences(p Preferences, themePath string, save func(Preferences) error) error {
	s.preferences, s.savePreferences, s.themePath = p, save, themePath
	if themePath != "" && p.Theme != "" {
		_, err := saveTheme(themePath, p.Theme, true)
		if err == nil {
			s.preferences.Theme = ""
		}
		return err
	}
	return nil
}

func (s *Server) currentPreferences() Preferences {
	s.prefMu.Lock()
	defer s.prefMu.Unlock()
	p := s.preferences
	if s.themePath != "" {
		shared, _ := ReadTheme(s.themePath)
		p.Theme = cmp.Or(shared, p.Theme)
	}
	return p
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
	if !validTheme(p.Theme) {
		writeError(w, http.StatusBadRequest, "unknown theme")
		return
	}
	s.prefMu.Lock()
	defer s.prefMu.Unlock()
	importing := r.Method == http.MethodPost
	var shared string
	if s.themePath != "" {
		if p.Theme == "" {
			shared, _ = ReadTheme(s.themePath)
		} else {
			var err error
			shared, err = saveTheme(s.themePath, p.Theme, importing)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "couldn't save page preferences")
				return
			}
		}
	}
	was := s.preferences
	next := Preferences{cmp.Or(p.Theme, was.Theme), cmp.Or(p.OnlyMine, was.OnlyMine), cmp.Or(p.TagsFolded, was.TagsFolded)}
	if importing {
		next = Preferences{cmp.Or(was.Theme, p.Theme), cmp.Or(was.OnlyMine, p.OnlyMine), cmp.Or(was.TagsFolded, p.TagsFolded)}
	}
	if s.themePath != "" {
		// Keep an old clone theme on disk until the shared file can take it.
		next.Theme = was.Theme
		if shared != "" || p.Theme != "" {
			next.Theme = ""
		}
	}
	if next != was && s.savePreferences != nil {
		if err := s.savePreferences(next); err != nil {
			writeError(w, http.StatusInternalServerError, "couldn't save page preferences")
			return
		}
	}
	s.preferences = next
	next.Theme = cmp.Or(shared, next.Theme)
	writeJSON(w, next)
}
