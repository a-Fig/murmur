package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fakeUpdates struct {
	setting  UpdateSetting
	sets     []string
	backs    int
	installs []string
	refuse   error // InstallNow's answer
}

func (f *fakeUpdates) InstallNow(_ context.Context, version string) error {
	if f.refuse != nil {
		return f.refuse
	}
	f.installs = append(f.installs, version)
	return nil
}

func (f *fakeUpdates) UpdateSetting(context.Context) (UpdateSetting, error) { return f.setting, nil }

func (f *fakeUpdates) SetUpdates(_ context.Context, choice string) (UpdateSetting, error) {
	f.sets = append(f.sets, choice)
	f.setting.Choice, f.setting.Effective = choice, choice
	return f.setting, nil
}

func (f *fakeUpdates) BackToStable(context.Context) (UpdateSetting, error) {
	f.backs++
	f.setting.CanGoBack, f.setting.Hold = false, "Going back to the latest stable release."
	return f.setting, nil
}

func startUpdates(t *testing.T, u Updates) *harness {
	t.Helper()
	server := NewWithPRs(newFake(), testToken, noPRs)
	if u != nil {
		server.WithUpdates(u)
	}
	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)
	addr, _ := url.Parse(srv.URL)
	return &harness{t: t, srv: srv, port: addr.Port()}
}

// The update setting is read and changed only with the page's token, from a page on its own
// loopback address, and only to one of its three choices (#1045).
func TestUpdatesEndpoint(t *testing.T) {
	f := &fakeUpdates{setting: UpdateSetting{Choice: UpdatesOn, Effective: UpdatesOff, Why: "Off here: this repo's git config sets murmur.update.", CanGoBack: true}}
	h := startUpdates(t, f)
	host := "127.0.0.1:" + h.port
	if got := decode[UpdateSetting](t, h.ok(http.MethodGet, "/api/updates", "")); got != f.setting {
		t.Fatalf("got %+v, want %+v", got, f.setting)
	}
	for _, c := range []struct {
		name, method, path, host, token, body string
		code                                  int
	}{
		{"no token", http.MethodPut, "/api/updates", host, "", `{"choice":"off"}`, http.StatusUnauthorized},
		{"wrong token", http.MethodPut, "/api/updates", host, "nope", `{"choice":"off"}`, http.StatusUnauthorized},
		{"another site's name for loopback", http.MethodPut, "/api/updates", "evil.example:" + h.port, testToken, `{"choice":"off"}`, http.StatusMisdirectedRequest},
		{"no token to go back", http.MethodPost, "/api/updates/back", host, "", "", http.StatusUnauthorized},
		{"no token to read", http.MethodGet, "/api/updates", host, "", "", http.StatusUnauthorized},
		{"unknown choice", http.MethodPut, "/api/updates", host, testToken, `{"choice":"always"}`, http.StatusBadRequest},
		{"a choice in another case", http.MethodPut, "/api/updates", host, testToken, `{"choice":"Off"}`, http.StatusBadRequest},
		{"no choice", http.MethodPut, "/api/updates", host, testToken, `{}`, http.StatusBadRequest},
		{"another key", http.MethodPut, "/api/updates", host, testToken, `{"choice":"on","key":"core.hooksPath"}`, http.StatusBadRequest},
		{"not JSON", http.MethodPut, "/api/updates", host, testToken, `choice=on`, http.StatusBadRequest},
		{"no token to install", http.MethodPost, "/api/updates/install", host, "", `{"version":"0.4.0"}`, http.StatusUnauthorized},
		{"another site's name, to install", http.MethodPost, "/api/updates/install", "evil.example:" + h.port, testToken, `{"version":"0.4.0"}`, http.StatusMisdirectedRequest},
		{"install, no version", http.MethodPost, "/api/updates/install", host, testToken, `{}`, http.StatusBadRequest},
		{"install, no body", http.MethodPost, "/api/updates/install", host, testToken, ``, http.StatusBadRequest},
		{"install, a v", http.MethodPost, "/api/updates/install", host, testToken, `{"version":"v0.4.0"}`, http.StatusBadRequest},
		{"install, a URL", http.MethodPost, "/api/updates/install", host, testToken, `{"version":"https://evil.example/mur"}`, http.StatusBadRequest},
		{"install, a path", http.MethodPost, "/api/updates/install", host, testToken, `{"version":"0.4.0-../../x"}`, http.StatusBadRequest},
		{"install, another key", http.MethodPost, "/api/updates/install", host, testToken, `{"version":"0.4.0","url":"https://evil.example"}`, http.StatusBadRequest},
		{"install with GET", http.MethodGet, "/api/updates/install", host, testToken, ``, http.StatusMethodNotAllowed},
	} {
		resp := h.do(c.method, c.path, c.host, c.token, c.body)
		if resp.StatusCode != c.code {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("%s: %d %s, want %d", c.name, resp.StatusCode, b, c.code)
		}
	}
	if len(f.sets) != 0 || f.backs != 0 || len(f.installs) != 0 {
		t.Fatalf("refused requests reached the setting: sets %v, backs %d, installs %v", f.sets, f.backs, f.installs)
	}
	for _, choice := range []string{UpdatesOff, UpdatesNightly, UpdatesOn} {
		got := decode[UpdateSetting](t, h.ok(http.MethodPut, "/api/updates", `{"choice":"`+choice+`"}`))
		if got.Choice != choice {
			t.Errorf("PUT %s answered %+v", choice, got)
		}
	}
	if got := decode[UpdateSetting](t, h.ok(http.MethodPost, "/api/updates/back", "")); got.CanGoBack || got.Hold == "" || f.backs != 1 {
		t.Errorf("going back answered %+v after %d calls", got, f.backs)
	}
	if len(f.sets) != 3 {
		t.Errorf("sets %v", f.sets)
	}
	for _, v := range []string{"0.4.0", "0.4.0-nightly.20261122", "0.4.0-rc.1"} {
		if resp := h.do(http.MethodPost, "/api/updates/install", host, testToken, `{"version":"`+v+`"}`); resp.StatusCode != http.StatusNoContent {
			t.Errorf("install %s: %d", v, resp.StatusCode)
		}
	}
	if len(f.installs) != 3 || f.installs[0] != "0.4.0" {
		t.Errorf("installs %v", f.installs)
	}
	f.refuse = errors.New("0.4.1 is the newer release now, not 0.4.0")
	resp := h.do(http.MethodPost, "/api/updates/install", host, testToken, `{"version":"0.4.0"}`)
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "0.4.1 is the newer release") {
		t.Errorf("a refused install: %d %s", resp.StatusCode, b)
	}
}

// A page with no update setting, as the tests' and a source's without one, says so rather than
// showing a choice it can't change.
func TestNoUpdatesSetting(t *testing.T) {
	h := startUpdates(t, nil)
	for _, r := range [][2]string{{http.MethodGet, "/api/updates"}, {http.MethodPut, "/api/updates"}, {http.MethodPost, "/api/updates/back"}, {http.MethodPost, "/api/updates/install"}} {
		if resp := h.do(r[0], r[1], "127.0.0.1:"+h.port, testToken, `{"choice":"on"}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", r[0], r[1], resp.StatusCode)
		}
	}
}
