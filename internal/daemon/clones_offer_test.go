package daemon_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/web"
)

func setSlugOrigin(t *testing.T, d *device, remote, slug string) {
	t.Helper()
	dir := filepath.Dir(d.gitDir)
	address := "git@github.com:" + slug + ".git"
	git(t, dir, "config", "remote.origin.url", address)
	git(t, dir, "config", "url."+remote+".insteadOf", address)
}

func setNick(t *testing.T, p *page, nick string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"nick": nick})
	code, body := p.call("PUT", "/api/nick", p.token, string(b))
	if code != http.StatusOK {
		t.Fatalf("PUT /api/nick %q: %d %s", nick, code, body)
	}
}

func shiftCloneStateTime(t *testing.T, d *device, at time.Time) {
	t.Helper()
	path := filepath.Join(d.gitDir, brand.StateDir, "state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	m["lastSync"] = stamp
	m["personSince"] = stamp
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestCrossRepoOfferNameAndNickAcrossRepos(t *testing.T) {
	clonesDir := t.TempDir()
	r1 := newRemote(t)
	r2 := newRemote(t)

	d1 := newDeviceWith(t, r1, func(d *device) {
		d.clones = clonesDir
		setSlugOrigin(t, d, r1, "a-Fig/murmur")
	})
	p1 := d1.page()
	p1.choose("tyler")
	setNick(t, p1, "tyler-laptop")
	d1.stop() // stopped first clone must still be offered (#1277)

	d2 := newDeviceWith(t, r2, func(d *device) {
		d.clones = clonesDir
		setSlugOrigin(t, d, r2, "a-Fig/docs")
	})
	p2 := d2.page()

	var offers []web.AccountOffer
	eventually(t, "cross-repo offer appears", func() bool {
		offers = p2.state().Account.Offers
		return len(offers) == 1
	})
	if offers[0].Name != "tyler" || offers[0].Nick != "tyler-laptop" || offers[0].Repo != "a-Fig/murmur" || offers[0].Taken != "" || offers[0].Problem != "" || offers[0].NickProblem != "" {
		t.Fatalf("unexpected offer: %+v", offers[0])
	}

	// Accept in one call as the page does; must claim name, set nick, and clear offers.
	code, body := p2.call("PUT", "/api/account", p2.token, `{"name":"tyler","nick":"tyler-laptop"}`)
	if code != http.StatusOK {
		t.Fatalf("accept offer: %d %s", code, body)
	}
	var acct web.Account
	if err := json.Unmarshal([]byte(body), &acct); err != nil {
		t.Fatal(err)
	}
	if acct.Name != "tyler" || acct.Nick != "tyler-laptop" || len(acct.Offers) != 0 {
		t.Fatalf("accepted account = %+v", acct)
	}
	st := p2.state()
	if len(st.Account.Offers) != 0 {
		t.Fatalf("offers stayed populated after naming: %+v", st.Account.Offers)
	}
	if !st.Devices[0].Self || st.Devices[0].Nick != "tyler-laptop" || st.Devices[0].Person == nil || st.Devices[0].Person.Name != "tyler" {
		t.Fatalf("self device after accept = %+v", st.Devices[0])
	}
}

func TestCrossRepoOfferWithoutNickKeepsDefaultDeviceName(t *testing.T) {
	clonesDir := t.TempDir()
	r1 := newRemote(t)
	r2 := newRemote(t)

	d1 := newDeviceWith(t, r1, func(d *device) {
		d.clones = clonesDir
		setSlugOrigin(t, d, r1, "a-Fig/murmur")
	})
	d1.page().choose("tyler")
	d1.stop()

	d2 := newDeviceWith(t, r2, func(d *device) {
		d.clones = clonesDir
		setSlugOrigin(t, d, r2, "a-Fig/site")
	})
	p2 := d2.page()

	eventually(t, "offer without nick", func() bool {
		return len(p2.state().Account.Offers) == 1
	})
	if got := p2.state().Account.Offers[0]; got.Name != "tyler" || got.Nick != "" {
		t.Fatalf("unexpected offer: %+v", got)
	}

	p2.choose("tyler")
	st := p2.state()
	if st.Account.Nick != "" || st.Devices[0].Nick != "tyler" {
		t.Fatalf("expected default device nick = person name, got account.Nick=%q device.Nick=%q", st.Account.Nick, st.Devices[0].Nick)
	}
}

func TestCrossRepoOfferTakenFallsBackAndFiltersConflictingNick(t *testing.T) {
	t.Parallel()
	clonesDir := t.TempDir()
	r1 := newRemote(t)
	r2 := newRemote(t)
	r3 := newRemote(t)

	// User picked ("tyler", "tyler-laptop") in r1, and ("ada", "busy-nick") in r3.
	d1 := newDeviceWith(t, r1, func(d *device) {
		d.clones = clonesDir
		setSlugOrigin(t, d, r1, "a-Fig/murmur")
	})
	p1 := d1.page()
	p1.choose("tyler")
	setNick(t, p1, "tyler-laptop")
	d1.stop()

	d3 := newDeviceWith(t, r3, func(d *device) {
		d.clones = clonesDir
		setSlugOrigin(t, d, r3, "a-Fig/cli")
	})
	p3 := d3.page()
	p3.choose("ada")
	setNick(t, p3, "busy-nick")
	d3.stop()

	shiftCloneStateTime(t, d1, time.Now().Add(-10*time.Minute))
	shiftCloneStateTime(t, d3, time.Now().Add(-30*time.Minute))

	// Peers in r2 already hold "tyler" and "busy-nick".
	r2Peer := newDevice(t, r2)
	r2Peer.page().choose("tyler")
	r2PeerNick := newDevice(t, r2)
	r2PeerNick.page().choose("busy-nick")

	d2 := newDeviceWith(t, r2, func(d *device) {
		d.clones = clonesDir
		git(t, filepath.Dir(d.gitDir), "config", "user.name", "Tyler Fig")
	})
	p2 := d2.page()

	var st web.State
	eventually(t, "peer claims visible in offers", func() bool {
		st = p2.state()
		return len(st.Account.Offers) == 2 && st.Account.Offers[0].Taken != "" && st.Account.Offers[1].NickProblem != ""
	})
	if st.Account.Suggested != "tyler-fig" {
		t.Fatalf("Suggested = %q, want tyler-fig", st.Account.Suggested)
	}
	if off := st.Account.Offers[0]; off.Name != "tyler" || off.Taken != r2Peer.self() || off.Nick != "tyler-laptop" {
		t.Fatalf("first offer = %+v, want taken by %s with free nick tyler-laptop", off, r2Peer.self())
	}
	if off := st.Account.Offers[1]; off.Name != "ada" || off.Nick != "" || !strings.Contains(off.NickProblem, "busy-nick") {
		t.Fatalf("second offer = %+v, want nick cleared with NickProblem mentioning busy-nick", off)
	}

	// Refusing an invalid/colliding nick during SetAccount must NOT leave name claimed.
	code, _ := p2.call("PUT", "/api/account", p2.token, `{"name":"tyler-fig","nick":"busy-nick"}`)
	if code == http.StatusOK {
		t.Fatalf("expected conflicting nick to fail SetAccount")
	}
	if after := p2.state().Account.Name; after != "" {
		t.Fatalf("account was claimed (%q) despite invalid nick", after)
	}
}

func TestCrossRepoOfferOrdersByRecencyDedupesAndSkipsLeftClones(t *testing.T) {
	clonesDir := t.TempDir()
	now := time.Now()

	setupClone := func(slug, person, nick string, age time.Duration) *device {
		r := newRemote(t)
		d := newDeviceWith(t, r, func(d *device) {
			d.clones = clonesDir
			setSlugOrigin(t, d, r, slug)
		})
		p := d.page()
		p.choose(person)
		if nick != "" {
			setNick(t, p, nick)
		}
		d.stop()
		shiftCloneStateTime(t, d, now.Add(-age))
		return d
	}

	_ = setupClone("a-Fig/alpha", "alex", "alex-desk", 3*time.Hour)
	_ = setupClone("a-Fig/beta", "tyler", "tyler-laptop", 2*time.Hour)
	_ = setupClone("a-Fig/gamma", "tyler", "tyler-laptop", 1*time.Hour)
	dLeft := setupClone("a-Fig/delta", "left-person", "left-nick", 5*time.Minute)

	// A clone whose person lost its claim in that repo must not be offered either.
	dStaleClaim := setupClone("a-Fig/epsilon", "lost-person", "lost-nick", 2*time.Minute)
	stalePath := filepath.Join(dStaleClaim.gitDir, brand.StateDir, "state.json")
	raw, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	var stMap map[string]any
	if err := json.Unmarshal(raw, &stMap); err != nil {
		t.Fatal(err)
	}
	stMap["personClaims"] = map[string]any{"lost-person": "other-dev-0001/human"}
	updated, err := json.Marshal(stMap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, updated, 0o600); err != nil {
		t.Fatal(err)
	}

	rTarget := newRemote(t)
	dTarget := newDeviceWith(t, rTarget, func(d *device) {
		d.clones = clonesDir
		d.joined = func(dir string) bool {
			return dir != dLeft.gitDir
		}
	})

	var offers []web.AccountOffer
	eventually(t, "ordered deduped offers", func() bool {
		offers = dTarget.page().state().Account.Offers
		return len(offers) == 2
	})
	if offers[0].Name != "tyler" || offers[0].Repo != "a-Fig/gamma" {
		t.Fatalf("offers[0] = %+v, want newest duplicate from a-Fig/gamma", offers[0])
	}
	if offers[1].Name != "alex" || offers[1].Repo != "a-Fig/alpha" {
		t.Fatalf("offers[1] = %+v, want older alex from a-Fig/alpha", offers[1])
	}
}
