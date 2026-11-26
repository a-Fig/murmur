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

// Murmur Night, the v3 design's dark version, is a theme the page can save, and the desktop window
// opens dark for it before the page loads.
func TestNightIsADarkTheme(t *testing.T) {
	if !validTheme("night") {
		t.Fatal("night should be a theme the page can save")
	}
	if !DarkFrame("night") {
		t.Fatal("the desktop window should open dark for Murmur Night")
	}
	if validTheme("midnight") {
		t.Fatal("an unknown theme should be refused")
	}
}

// Murmur Day, the v3 design's light version, is a theme the page can save, and the desktop window
// opens light for it before the page loads.
func TestDayIsALightTheme(t *testing.T) {
	if !validTheme("day") {
		t.Fatal("day should be a theme the page can save")
	}
	if DarkFrame("day") {
		t.Fatal("the desktop window should open light for Murmur Day")
	}
}

// The removed Murmur theme is refused for new selections, and any saved copy (the shared theme
// file, an older clone's preferences file, or a browser migration POST) lands on Dim (#1182).
func TestRemovedMurmurThemeMigratesToDim(t *testing.T) {
	if validTheme("murmur") {
		t.Fatal("murmur should no longer be a valid theme")
	}

	dir := t.TempDir()
	themePath := filepath.Join(dir, "app", "theme")
	if err := os.MkdirAll(filepath.Dir(themePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(themePath, []byte("murmur\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTheme(themePath)
	if err != nil || got != "dim" {
		t.Fatalf("ReadTheme(murmur) = %q, %v; want dim", got, err)
	}
	if !DarkFrame(got) {
		t.Fatal("the desktop window should open dark for migrated murmur -> dim")
	}

	server := NewWithPRs(newFake(), testToken, noPRs)
	if err := server.WithPreferences(Preferences{}, themePath, nil); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	h := &harness{t: t, srv: httpServer}

	resp := h.do(http.MethodGet, "/", "", "", "")
	html, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(html), `data-theme="dim" data-theme-saved`) {
		t.Fatalf("saved murmur theme should render in Dim as saved: status %d, err %v, html %q", resp.StatusCode, err, string(html))
	}
	if p := decode[Preferences](t, h.do(http.MethodPost, "/api/preferences", "", testToken, `{"theme":"sky"}`)); p.Theme != "dim" {
		t.Fatalf("a stale browser import should keep migrated dim, got %+v", p)
	}
	if resp := h.do(http.MethodPatch, "/api/preferences", "", testToken, `{"theme":"murmur"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PATCH murmur should be refused with 400, got %d", resp.StatusCode)
	}

	// An older clone's page-preferences.json with theme "murmur" also migrates to dim.
	cloneDir := filepath.Join(dir, "clone")
	if err := os.MkdirAll(cloneDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, PreferencesFile), []byte(`{"theme":"murmur","onlyMine":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prefs, err := ReadPreferences(cloneDir)
	if err != nil || prefs.Theme != "dim" || prefs.OnlyMine == nil || !*prefs.OnlyMine {
		t.Fatalf("ReadPreferences(murmur) = %+v, %v; want dim and onlyMine=true", prefs, err)
	}

	// A browser migrating legacy localStorage via POST with theme "murmur" lands on dim.
	freshPath := filepath.Join(dir, "fresh", "theme")
	freshServer := NewWithPRs(newFake(), testToken, noPRs)
	if err := freshServer.WithPreferences(Preferences{}, freshPath, nil); err != nil {
		t.Fatal(err)
	}
	freshHTTP := httptest.NewServer(freshServer)
	t.Cleanup(freshHTTP.Close)
	fh := &harness{t: t, srv: freshHTTP}
	if p := decode[Preferences](t, fh.do(http.MethodPost, "/api/preferences", "", testToken, `{"theme":"murmur"}`)); p.Theme != "dim" {
		t.Fatalf("POST murmur migration = %+v; want dim", p)
	}
}
