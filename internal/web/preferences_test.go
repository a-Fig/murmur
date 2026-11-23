package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedThemeAcrossServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app", "theme")
	startClone := func(saved Preferences) *harness {
		server := NewWithPRs(newFake(), testToken, noPRs)
		if err := server.WithPreferences(saved, path, func(next Preferences) error {
			if next.Theme != "" {
				t.Error("clone save included the shared theme")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewServer(server)
		t.Cleanup(httpServer.Close)
		return &harness{t: t, srv: httpServer}
	}
	a := startClone(Preferences{Theme: "sky"})
	b := startClone(Preferences{Theme: "dark"})
	const endpoint = "/api/preferences"
	get := func(h *harness) Preferences {
		return decode[Preferences](t, h.do(http.MethodGet, endpoint, "", testToken, ""))
	}
	if p := get(b); p.Theme != "sky" {
		t.Fatalf("the first clone's old theme should win, got %q", p.Theme)
	}
	decode[Preferences](t, a.do(http.MethodPatch, endpoint, "", testToken, `{"theme":"light","onlyMine":true}`))
	p := decode[Preferences](t, b.do(http.MethodPost, endpoint, "", testToken, `{"theme":"dark","tagsFolded":true}`))
	if p.Theme != "light" || p.OnlyMine != nil || p.TagsFolded == nil || !*p.TagsFolded {
		t.Fatalf("stale migration should keep the new theme and fill only this clone's unset filter: %+v", p)
	}
	if p := get(a); p.OnlyMine == nil || !*p.OnlyMine || p.TagsFolded != nil {
		t.Fatalf("first clone's filters: %+v", p)
	}
	resp := b.do(http.MethodGet, "/", "", "", "")
	html, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(html), `data-theme="light"`) {
		t.Fatalf("second clone should paint in Light before JavaScript: status %d, error %v", resp.StatusCode, err)
	}
}

func TestFilterSaveWhenThemeFileIsUnavailable(t *testing.T) {
	// A directory in place of the theme file makes reads fail even when tests run as root.
	path := filepath.Join(t.TempDir(), "theme")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var saved Preferences
	var server Server
	if err := server.WithPreferences(Preferences{Theme: "sky"}, path, func(p Preferences) error {
		saved = p
		return nil
	}); err == nil {
		t.Fatal("import should fail when the theme file can't be read")
	}
	w := httptest.NewRecorder()
	server.updatePreferences(w, httptest.NewRequest(http.MethodPatch, "/api/preferences", strings.NewReader(`{"onlyMine":true}`)))
	if w.Code != http.StatusOK || saved.Theme != "sky" || saved.OnlyMine == nil || !*saved.OnlyMine {
		t.Fatalf("filter should save without losing the old theme: status %d, saved %+v", w.Code, saved)
	}
}

// Night, the v3 design's dark version, is a theme the page can save, and the desktop window opens
// dark for it before the page loads.
func TestNightIsADarkTheme(t *testing.T) {
	if !validTheme("night") {
		t.Fatal("night should be a theme the page can save")
	}
	if !DarkFrame("night") {
		t.Fatal("the desktop window should open dark for Night")
	}
	if validTheme("midnight") {
		t.Fatal("an unknown theme should be refused")
	}
}
