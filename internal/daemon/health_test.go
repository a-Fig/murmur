package daemon_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of the health snapshot (GET /api/health): a device whose sync with the remote fails says
// so, and says when it works again, without a person reading daemon.log (#141).

func (p *page) health() (web.Health, string) {
	p.t.Helper()
	var h web.Health
	body := p.get("/api/health", &h)
	return h, body
}

// moveDir renames a directory, again and again for a while on Windows, where a git still at work in
// it can hold it open.
func moveDir(t *testing.T, from, to string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := os.Rename(from, to)
		if err == nil {
			return
		}
		if runtime.GOOS != "windows" || time.Now().After(deadline) {
			t.Fatalf("moving %s: %v", from, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The remote goes away and comes back. While it is away the snapshot says since when the syncs have
// failed, how many in a row, and why, and that the presence pushes fail too; once it is back, the
// syncs work again and the last error is kept, dated, for whoever looks after the fact.
func TestHealthReportsAFailingSyncAndItsRecovery(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	d := newDevice(t, remote)
	d.session("aaaaaa", "")
	p := d.page()
	eventually(t, "a sync that worked", func() bool {
		h, _ := p.health()
		return !h.Sync.LastOK.IsZero() && h.Sync.LastOKAge >= 0 && h.Pushes.Presence.Pushed > 0
	})
	h, _ := p.health()
	if h.Sync.Failures != 0 || !h.Sync.FailingSince.IsZero() || h.Sync.LastError != nil {
		t.Fatalf("a device that has had no trouble: %+v", h.Sync)
	}
	if h.Daemon.Started.IsZero() || h.Daemon.Protocol != proto.Protocol || h.Daemon.Poll == "" {
		t.Errorf("the daemon: %+v", h.Daemon)
	}
	if h.PublicRepo.State != web.RepoDev {
		t.Errorf("a daemon with no version (a test's) reports nothing, but the snapshot says %+v", h.PublicRepo)
	}

	away := remote + ".away"
	before := time.Now()
	moveDir(t, remote, away)
	eventually(t, "the failing sync to show", func() bool {
		h, _ = p.health()
		return h.Sync.Failures >= 2 && h.Pushes.Presence.Failed > 0
	})
	if h.Sync.FailingSince.Before(before) || h.Sync.FailingSince.After(time.Now()) {
		t.Errorf("failing since %v, but the remote went away at %v", h.Sync.FailingSince, before)
	}
	if e := h.Sync.LastError; e == nil || e.Class != web.ErrOther || !strings.Contains(e.Message, "does not appear to be a git repository") {
		t.Errorf("the sync's last error: %+v", e)
	}
	if !h.Sync.LastOK.Before(h.Sync.FailingSince) || h.Sync.Failed < int64(h.Sync.Failures) {
		t.Errorf("the last sync that worked is %v, failing since %v, %d failed in all", h.Sync.LastOK, h.Sync.FailingSince, h.Sync.Failed)
	}
	if e := h.Pushes.Presence.LastError; e == nil || e.Message == "" {
		t.Errorf("the presence push's last error: %+v", e)
	}

	moveDir(t, away, remote)
	eventually(t, "the sync to work again", func() bool {
		h, _ = p.health()
		return h.Sync.Failures == 0 && h.Sync.LastOK.After(before)
	})
	if !h.Sync.FailingSince.IsZero() || h.Sync.LastError == nil || h.Sync.LastError.Class != web.ErrOther {
		t.Errorf("after the outage: failing since %v, last error %+v", h.Sync.FailingSince, h.Sync.LastError)
	}
}

// A remote whose hook refuses every push: the device's presence pushes show as refused by the
// server, apart from refusals because the ref had moved, and count again as pushed once the hook
// lets them through.
func TestHealthCountsRefusedPushes(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	d := newDevice(t, remote)
	p := d.page()
	eventually(t, "presence pushed", func() bool {
		h, _ := p.health()
		return h.Pushes.Presence.Pushed > 0
	})
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho no pushes today >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d.session("aaaaaa", "") // a change of presence, pushed at once
	var h web.Health
	eventually(t, "a refused push", func() bool {
		h, _ = p.health()
		return h.Pushes.Presence.Refused > 0
	})
	if e := h.Pushes.Presence.LastError; e == nil || e.Class != web.ErrRejected {
		t.Errorf("the refused push's error: %+v", e)
	}
	if h.Pushes.Presence.LeaseRefused != 0 {
		t.Errorf("a refusal by the server counted as the ref having moved: %+v", h.Pushes.Presence)
	}
	if h.Sync.Failures != 0 {
		t.Errorf("the hook refuses pushes, not listings, and the syncs failed: %+v", h.Sync)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	pushed := h.Pushes.Presence.Pushed
	eventually(t, "presence pushed again", func() bool {
		h, _ = p.health()
		return h.Pushes.Presence.Pushed > pushed
	})
}

// A remote URL with credentials in it never reaches the snapshot, however git words its failure.
// And the snapshot is the page's: no token, or a wrong one, gets nothing, as with /api/state.
func TestHealthHasNoCredentialsAndNeedsTheToken(t *testing.T) {
	remote := newRemote(t)
	d := newDevice(t, remote)
	p := d.page()
	for _, token := range []string{"", "wrong", p.token + "x"} {
		if code, body := p.call("GET", "/api/health", token, ""); code != http.StatusUnauthorized || strings.Contains(body, "sync") {
			t.Errorf("GET /api/health with token %q: %d %s", token, code, body)
		}
		if code, _ := p.call("GET", "/api/state", token, ""); code != http.StatusUnauthorized {
			t.Errorf("GET /api/state with token %q: %d", token, code)
		}
	}

	const secret = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	// Port 1 refuses at once, so nothing reaches the network.
	git(t, filepath.Dir(d.gitDir), "remote", "set-url", "origin", "http://x-access-token:"+secret+"@127.0.0.1:1/a-Fig/murmur.git")
	d.session("aaaaaa", "")
	var body string
	eventually(t, "the failing sync to show", func() bool {
		var h web.Health
		h, body = p.health()
		return h.Sync.Failures >= 1 && h.Pushes.Presence.Failed > 0
	})
	for _, leak := range []string{secret, "x-access-token"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the snapshot has %q in it:\n%s", leak, body)
		}
	}
	var h web.Health
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatal(err)
	}
	if h.Sync.LastError == nil || !strings.Contains(h.Sync.LastError.Message, "127.0.0.1") {
		t.Errorf("the sync's error should still say where it failed: %+v", h.Sync.LastError)
	}
}

// Messages the hook shows as notices are counted, and so is the push to this device's log that
// sent them.
func TestHealthCountsNotices(t *testing.T) {
	remote := newRemote(t)
	d := newDevice(t, remote)
	d.session("aaaaaa", "")
	bob, _ := d.session("bbbbbb", "claude-bob")
	p := d.page()
	// The device pushes one ref at a time, so a send made now would wait behind the sessions'
	// start events, which on a loaded machine can take longer than a request may.
	eventually(t, "both sessions' starts pushed", func() bool {
		h, _ := p.health()
		return h.Pushes.Mesh.Pushed >= 2
	})
	if r := d.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "lunch?"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the notice", func() bool {
		return len(d.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"}).Messages) > 0
	})
	h, _ := p.health()
	if h.Delivery.Hook.Notices < 1 || h.Pushes.Mesh.Pushed < 1 {
		t.Errorf("after a notice: delivery %+v, mesh pushes %+v", h.Delivery, h.Pushes.Mesh)
	}
}
