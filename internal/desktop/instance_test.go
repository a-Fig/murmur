package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A second `mur app` finds the open window's record, and the window comes forward; a record with
// the wrong key, or one whose window has gone, brings nothing forward.
func TestBringForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	if BringForward(path) {
		t.Fatal("brought forward with no window")
	}
	var raised atomic.Int32
	stop, err := listenInstance(path, func() { raised.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if !BringForward(path) || raised.Load() != 1 {
		t.Fatalf("brought forward: raised %d times", raised.Load())
	}

	b, _ := os.ReadFile(path)
	var rec instance
	json.Unmarshal(b, &rec)
	wrong := filepath.Join(t.TempDir(), "window.json")
	rec.Key = "not-the-key"
	b, _ = json.Marshal(rec)
	os.WriteFile(wrong, b, 0o600)
	if BringForward(wrong) || raised.Load() != 1 {
		t.Fatal("a wrong key brought the window forward")
	}

	stop()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the record outlived its window: %v", err)
	}
	b, _ = json.Marshal(instance{PID: 1, Addr: rec.Addr, Key: rec.Key})
	os.WriteFile(path, b, 0o600)
	if BringForward(path) {
		t.Fatal("brought forward a window that has gone")
	}
}
