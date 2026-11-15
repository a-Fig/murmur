package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

// joinedClone is a clone with a remote, its repo set up, and the user joined in it, but no daemon.
func joinedClone(t *testing.T) string {
	t.Helper()
	_, clone := cloneOf(t, t.TempDir())
	t.Cleanup(func() { stopDaemon(clone) })
	ctx := context.Background()
	if _, err := setup.Init(ctx, clone); err != nil {
		t.Fatal(err)
	}
	p, err := join.Locate(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	return clone
}

// call makes one request of a page, with its token if given, and decodes the answer into out.
func call(t *testing.T, method, address, token, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, address, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// From one clone's page, the user sees this machine's other joined clone and moves to its page
// (#348): the first daemon starts the second, and the tab gets a key, not a token, which the second
// page trades once for its own token.
func TestThePageSwitchesToAnotherClone(t *testing.T) {
	record := t.TempDir()
	t.Setenv(clones.DirEnv, record) // the daemons started here inherit it
	ctx := context.Background()
	a, b := joinedClone(t), joinedClone(t)
	gitA, err := client.FindGitDir(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	gitB, err := client.FindGitDir(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	// b joined, as `mur join` records it; its daemon hasn't run.
	if err := clones.Add(record, clones.Clone{GitDir: gitB, Name: clones.NameIn(ctx, gitB)}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runUI(ctx, []string{"-C", a, "--no-open"}, &out, nil); err != nil {
		t.Fatal(err)
	}
	pageA, err := url.Parse(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	tokenA := strings.TrimPrefix(pageA.Fragment, "token=")
	base := "http://" + pageA.Host

	var st struct {
		Repo string `json:"repo"`
	}
	if code := call(t, "GET", base+"/api/state", tokenA, "", &st); code != 200 || st.Repo != "remote" {
		t.Fatalf("state: %d, repo %q; want the remote's name", code, st.Repo)
	}

	// a's daemon puts a on the record as it starts, named; b is there from its join, not running.
	// The daemon answers its page before that write is done, so the list is read until a is on it
	// (#477: a slow runner saw only b).
	type repoList struct {
		Repos []struct {
			ID, Name, Path   string
			Running, Current bool
		}
	}
	var list repoList
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		list = repoList{}
		code := call(t, "GET", base+"/api/repos", tokenA, "", &list)
		if code == 200 && len(list.Repos) == 2 {
			break
		}
		if code != 200 || time.Now().After(deadline) {
			t.Fatalf("repos: %d, %+v", code, list.Repos)
		}
	}
	idB := clones.ID(gitB)
	for _, r := range list.Repos {
		if r.Current != (r.ID == clones.ID(gitA)) || r.Name != "remote" {
			t.Errorf("%+v: only a is current, and both are named for their remote", r)
		}
		if r.ID == idB && r.Running {
			t.Errorf("b's daemon is listed as running before it started")
		}
	}
	if list.Repos[0].ID != idB && list.Repos[1].ID != idB {
		t.Fatalf("b isn't listed: %+v", list.Repos)
	}

	var opened struct{ URL string }
	if code := call(t, "POST", base+"/api/repos/open", tokenA, `{"id":"`+idB+`"}`, &opened); code != 200 {
		t.Fatalf("open: %d", code)
	}
	pageB, err := url.Parse(opened.URL)
	key, ok := strings.CutPrefix(pageB.Fragment, "key=")
	if err != nil || !ok || pageB.Host == pageA.Host {
		t.Fatalf("open gave %q", opened.URL)
	}
	// The address carries no token, neither a's nor b's.
	infoB := readInfo(t, gitB)
	if strings.Contains(opened.URL, infoB.PageToken) || strings.Contains(opened.URL, infoB.Token) || strings.Contains(opened.URL, tokenA) {
		t.Fatalf("the address %q carries a token", opened.URL)
	}
	if pageB.Host != infoB.Web {
		t.Fatalf("the address is %s, and b's page is on %s", pageB.Host, infoB.Web)
	}

	baseB := "http://" + pageB.Host
	var traded struct{ Token string }
	if code := call(t, "POST", baseB+"/api/key", "", `{"key":"`+key+`"}`, &traded); code != 200 || traded.Token != infoB.PageToken {
		t.Fatalf("the key: %d, token %q; want b's page token", code, traded.Token)
	}
	if code := call(t, "POST", baseB+"/api/key", "", `{"key":"`+key+`"}`, nil); code != http.StatusUnauthorized {
		t.Errorf("the key was good twice: %d", code)
	}
	if code := call(t, "GET", baseB+"/api/state", traded.Token, "", nil); code != 200 {
		t.Errorf("b's page with the traded token: %d", code)
	}
	// a's key is no good on b's page, nor a's token.
	if code := call(t, "GET", baseB+"/api/state", tokenA, "", nil); code != http.StatusUnauthorized {
		t.Errorf("b's page took a's token: %d", code)
	}

	// Once b leaves, it is off the list, and can't be opened.
	if err := os.Remove(filepath.Join(gitB, "murmur", "joined.json")); err != nil {
		t.Fatal(err)
	}
	if code := call(t, "GET", base+"/api/repos", tokenA, "", &list); code != 200 || len(list.Repos) != 1 {
		t.Errorf("after b left, repos: %d, %+v", code, list.Repos)
	}
	if code := call(t, "POST", base+"/api/repos/open", tokenA, `{"id":"`+idB+`"}`, nil); code != http.StatusNotFound {
		t.Errorf("opening b after it left: %d, want 404", code)
	}
}

func readInfo(t *testing.T, gitDir string) proto.Info {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(client.StateDir(gitDir), "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info proto.Info
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	return info
}
