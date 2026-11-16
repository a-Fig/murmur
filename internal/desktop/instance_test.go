package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A second `mur app` finds the open window's record, and the window comes forward, with the repo
// it was asked for; asked before the window could come forward, it does once it can. A record with
// the wrong key, or one whose window has gone, brings nothing forward.
func TestBringForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	if BringForward(path, "") {
		t.Fatal("brought forward with no window")
	}
	in, err := Claim(path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var shown []string
	// Asked while the window is still being made: answered at once, done when it can be.
	if !BringForward(path, "/repos/early") {
		t.Fatal("a claimed window that isn't made yet didn't answer")
	}
	in.onShow(func(gitDir string) {
		mu.Lock()
		shown = append(shown, gitDir)
		mu.Unlock()
	})
	if !BringForward(path, "/repos/b") || !BringForward(path, "") {
		t.Fatal("not brought forward")
	}
	mu.Lock()
	if got := len(shown); got != 3 || shown[0] != "/repos/early" || shown[1] != "/repos/b" || shown[2] != "" {
		t.Fatalf("shown %q", shown)
	}
	mu.Unlock()

	b, _ := os.ReadFile(path)
	var rec instance
	json.Unmarshal(b, &rec)
	wrong := filepath.Join(t.TempDir(), "window.json")
	b, _ = json.Marshal(instance{PID: rec.PID, Addr: rec.Addr, Key: "not-the-key"})
	os.WriteFile(wrong, b, 0o600)
	if BringForward(wrong, "") {
		t.Fatal("a wrong key brought the window forward")
	}

	in.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the record outlived its window: %v", err)
	}
	b, _ = json.Marshal(rec)
	os.WriteFile(path, b, 0o600)
	if BringForward(path, "") {
		t.Fatal("brought forward a window that has gone")
	}
}

// A window that closes leaves a record another window wrote meanwhile.
func TestCloseLeavesAnotherWindowsRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	first, err := Claim(path)
	if err != nil || first == nil {
		t.Fatalf("claim: %v", err)
	}
	other, _ := json.Marshal(instance{PID: 1, Addr: "127.0.0.1:1", Key: "another"})
	os.WriteFile(path, other, 0o600)
	first.Close()
	if b, _ := os.ReadFile(path); string(b) != string(other) {
		t.Fatalf("the other window's record went with the first: %q", b)
	}
}

// Launches at once open one window: the others bring it forward.
func TestClaimOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	var wg sync.WaitGroup
	got := make(chan *Instance, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, err := Claim(path)
			if err != nil {
				t.Error(err)
			}
			got <- in
		}()
	}
	wg.Wait()
	close(got)
	n := 0
	for in := range got {
		if in != nil {
			n++
			defer in.Close()
		}
	}
	if n != 1 {
		t.Fatalf("%d windows claimed", n)
	}
}
