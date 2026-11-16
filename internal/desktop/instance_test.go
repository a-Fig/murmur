package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A second `mur app` finds the open window's record, and the window comes forward, with the repo
// it was asked for; asked before the window could come forward, it does once it can. A record with
// the wrong key, or one whose window has gone, brings nothing forward.
func TestBringForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	if BringForward(path, "") {
		t.Fatal("brought forward with no window")
	}
	in, err := Claim(path, "")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var shown []string
	// Asked while the window is still being made: answered once it is.
	early := make(chan bool)
	go func() { early <- BringForward(path, "/repos/early") }()
	waitAsked(t, in)
	in.onShow(func(gitDir string, reply func(bool)) {
		mu.Lock()
		shown = append(shown, gitDir)
		mu.Unlock()
		reply(true)
	})
	if !<-early {
		t.Fatal("a window asked for while it was being made didn't answer")
	}
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
	first, err := Claim(path, "")
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
			in, err := Claim(path, "")
			if err != nil {
				t.Error(err)
			}
			if in != nil {
				in.onShow(func(_ string, reply func(bool)) { reply(true) }) // the window is made, as a real one soon is
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

// waitAsked waits for a request to reach a window that isn't made yet.
func waitAsked(t *testing.T, in *Instance) {
	t.Helper()
	for range 200 {
		in.mu.Lock()
		asked := in.asked
		in.mu.Unlock()
		if asked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the request never came")
}

// A repo asked for before the window is made isn't undone by a later click that asks for none,
// and a window on its way out doesn't answer, so the one asking opens its own.
func TestPendingAndClosing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	in, err := Claim(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	first, second := make(chan bool), make(chan bool)
	go func() { first <- BringForward(path, "/repos/b") }()
	waitAsked(t, in)
	go func() { second <- BringForward(path, "") }()
	time.Sleep(100 * time.Millisecond)
	var got []string
	in.onShow(func(gitDir string, reply func(bool)) { got = append(got, gitDir); reply(true) })
	if !<-first || !<-second {
		t.Fatal("not answered once the window was made")
	}
	if len(got) != 1 || got[0] != "/repos/b" {
		t.Fatalf("shown %q", got)
	}
	in.closingDown()
	if BringForward(path, "") {
		t.Fatal("a closing window answered")
	}
}

// A window that fails while it is being made tells whoever was waiting for it that it didn't
// come forward, so they open their own.
func TestWindowThatFailsAnswersNo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	in, err := Claim(path, "")
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan bool)
	go func() { asked <- BringForward(path, "") }()
	waitAsked(t, in)
	in.Close()
	if <-asked {
		t.Fatal("a window that never came was said to have come forward")
	}
}

// A window whose own thread finds it closing when it comes to raise it says it didn't come
// forward, so the one asking opens its own.
func TestClosingOnTheWindowsThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	in, err := Claim(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	in.onShow(func(_ string, reply func(bool)) { go reply(false) })
	if BringForward(path, "") {
		t.Fatal("told it came forward, though the window was closing")
	}
}
