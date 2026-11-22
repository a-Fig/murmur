package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type fakeUpdates struct {
	setting UpdateSetting
	sets    []string
	backs   int
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
	} {
		resp := h.do(c.method, c.path, c.host, c.token, c.body)
		if resp.StatusCode != c.code {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("%s: %d %s, want %d", c.name, resp.StatusCode, b, c.code)
		}
	}
	if len(f.sets) != 0 || f.backs != 0 {
		t.Fatalf("refused requests reached the setting: sets %v, backs %d", f.sets, f.backs)
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
}

// A page with no update setting, as the tests' and a source's without one, says so rather than
// showing a choice it can't change.
func TestNoUpdatesSetting(t *testing.T) {
	h := startUpdates(t, nil)
	for _, r := range [][2]string{{http.MethodGet, "/api/updates"}, {http.MethodPut, "/api/updates"}, {http.MethodPost, "/api/updates/back"}} {
		if resp := h.do(r[0], r[1], "127.0.0.1:"+h.port, testToken, `{"choice":"on"}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", r[0], r[1], resp.StatusCode)
		}
	}
}
