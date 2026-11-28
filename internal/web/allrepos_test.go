package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func startAllRepos(t *testing.T, path string, changed func()) *harness {
	t.Helper()
	page := NewWithPRs(newFake(), testToken, noPRs)
	if path != "" {
		page.WithAllRepos(path, changed)
	}
	srv := httptest.NewServer(page)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &harness{t: t, srv: srv, port: u.Port()}
}

// The All repos tab's record (#816): each write sets only what it names, counts, and says which
// write set each setting, so a daemon takes what was set since the last write it took.
func TestAllReposRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clones", AllReposFile)
	if r, err := ReadAllRepos(path); err != nil || r.ID != "" {
		t.Fatalf("no record: %+v %v, want an empty one", r, err)
	}
	never, whole, on := Wake{When: WakeNever}, BusyWhole, true
	first, err := SetAllRepos(path, AllRepos{Wake: &never, SendFiles: &on})
	if err != nil {
		t.Fatal(err)
	}
	second, err := SetAllRepos(path, AllRepos{Busy: &whole})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Seq != first.Seq+1 || second.Settings.Wake == nil || *second.Settings.Busy != BusyWhole {
		t.Fatalf("the second write: %+v, want the first's settings kept and the busy choice added", second)
	}

	// A daemon that took the first write takes the busy choice alone.
	s := Settings{Wake: Wake{When: WakeAlways}, Busy: BusySubject}
	if !second.Since(&s, first.ID, first.Seq) || s.Busy != BusyWhole || s.Wake.When != WakeAlways || s.SendFiles || len(s.FilesSet) != 0 {
		t.Errorf("since the first write: %+v, want only busy set", s)
	}
	// One that took none, or another file's, takes them all, naming the file switch it sets.
	s = Settings{Wake: Wake{When: WakeAlways}}
	if !second.Since(&s, "another file", 9) || s.Wake.When != WakeNever || s.Busy != BusyWhole || !s.SendFiles || !slices.Equal(s.FilesSet, []string{"sendFiles"}) {
		t.Errorf("from another file: %+v, want everything set", s)
	}
	if second.Since(&s, second.ID, second.Seq) {
		t.Errorf("nothing is new since the last write, but Since copied something")
	}

	// A value the page wouldn't save is refused, and a file in a shape this doesn't know sets nothing.
	bad := Wake{When: WakeNever, Minutes: 3}
	if _, err := SetAllRepos(path, AllRepos{Wake: &bad}); err == nil {
		t.Errorf("a wake of never with minutes was saved")
	}
	if err := os.WriteFile(path, []byte(`{"id":"x","seq":3,"settings":{"busy":"loud"},"set":{"busy":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := ReadAllRepos(path); err != nil || r.ID != "" {
		t.Errorf("a record with a busy choice the page wouldn't save: %+v %v, want an empty one", r, err)
	}
}

func TestAllReposAPI(t *testing.T) {
	if resp := startAllRepos(t, "", nil).do("GET", "/api/settings/all", "", testToken, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a page without a record: %d, want 404, so it shows no All repos tab", resp.StatusCode)
	}
	path := filepath.Join(t.TempDir(), AllReposFile)
	changed := 0
	h := startAllRepos(t, path, func() { changed++ })
	if got := decode[AllRepos](t, h.ok("GET", "/api/settings/all", "")); got != (AllRepos{}) {
		t.Errorf("before any change: %+v, want nothing set", got)
	}
	if resp := h.do("PUT", "/api/settings/all", "127.0.0.1:"+h.port, testToken, `{"busy":"loud"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a busy choice the page wouldn't save: %d, want 400", resp.StatusCode)
	}
	if resp := h.do("PUT", "/api/settings/all", "127.0.0.1:"+h.port, testToken, `{"mode":"dnd"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("availability, which stays per repo: %d, want 400", resp.StatusCode)
	}
	got := decode[AllRepos](t, h.ok("PUT", "/api/settings/all", `{"receiveFiles":true}`))
	if got.ReceiveFiles == nil || !*got.ReceiveFiles || got.Wake != nil || changed != 1 {
		t.Errorf("after a change: %+v, %d calls of changed; want receiving on, nothing else, one call", got, changed)
	}
	if r, _ := ReadAllRepos(path); r.Set["receiveFiles"] != r.Seq || r.Seq != 1 {
		t.Errorf("the record: %+v", r)
	}
}
