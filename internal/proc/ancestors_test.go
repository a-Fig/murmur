package proc

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// This process's ancestors start with itself and go on to its parent.
func TestAncestorsStartWithTheProcessAndItsParent(t *testing.T) {
	got := Ancestors(os.Getpid(), 4)
	if len(got) < 2 || got[0] != os.Getpid() || got[1] != os.Getppid() {
		t.Fatalf("Ancestors(%d) = %v; want it, then its parent %d", os.Getpid(), got, os.Getppid())
	}
	if n := len(Ancestors(os.Getpid(), 1)); n != 1 {
		t.Errorf("max 1 gave %d", n)
	}
}

func TestWalkAncestorsGuards(t *testing.T) {
	parentCalls := 0
	noCalls := func(int) (int, bool) {
		parentCalls++
		return 99, true
	}
	if got := walkAncestors(0, 4, noCalls); got != nil {
		t.Errorf("pid 0: %v, want nil", got)
	}
	if got := walkAncestors(-5, 4, noCalls); got != nil {
		t.Errorf("negative pid: %v, want nil", got)
	}
	if got := walkAncestors(10, 0, noCalls); got != nil {
		t.Errorf("max 0: %v, want nil", got)
	}
	if got := walkAncestors(10, -1, noCalls); got != nil {
		t.Errorf("negative max: %v, want nil", got)
	}
	if got := walkAncestors(10, 1, noCalls); !slices.Equal(got, []int{10}) {
		t.Errorf("max 1: %v, want [10]", got)
	}
	if parentCalls != 0 {
		t.Errorf("max <= 1 or pid <= 0 invoked parent %d times, want 0", parentCalls)
	}

	// Cycles and self-parents stop without repeating.
	cycle := map[int]int{10: 20, 20: 10}
	if got := walkAncestors(10, 8, func(p int) (int, bool) { v, ok := cycle[p]; return v, ok }); !slices.Equal(got, []int{10, 20}) {
		t.Errorf("cycle: %v, want [10 20]", got)
	}
	if got := walkAncestors(10, 8, func(p int) (int, bool) { return p, true }); !slices.Equal(got, []int{10}) {
		t.Errorf("self-parent: %v, want [10]", got)
	}

	// Missing, zero, or negative parents stop cleanly; max truncates the chain.
	for _, stop := range []struct {
		name string
		fn   func(int) (int, bool)
	}{
		{"missing", func(int) (int, bool) { return 0, false }},
		{"zero", func(int) (int, bool) { return 0, true }},
		{"negative", func(int) (int, bool) { return -7, true }},
	} {
		if got := walkAncestors(10, 4, stop.fn); !slices.Equal(got, []int{10}) {
			t.Errorf("%s parent: %v, want [10]", stop.name, got)
		}
	}
	chain := map[int]int{10: 20, 20: 30, 30: 40}
	if got := walkAncestors(10, 3, func(p int) (int, bool) { v, ok := chain[p]; return v, ok }); !slices.Equal(got, []int{10, 20, 30}) {
		t.Errorf("truncated chain: %v, want [10 20 30]", got)
	}
}

func TestParseAndReadLinuxStatPPID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		want   int
		wantOK bool
	}{
		{"normal", "1234 (claude) S 567 1234 1234 0 -1\n", 567, true},
		{"spaces parens newline in comm", "1234 (my (app)\n v2) R 890 1234\n", 890, true},
		{"empty comm", "1234 () S 42 1234\n", 42, true},
		{"init ppid zero", "1 (systemd) S 0 1 1\n", 0, false},
		{"negative ppid", "1234 (x) S -5 1\n", 0, false},
		{"empty", "", 0, false},
		{"missing close paren", "1234 (claude S 567\n", 0, false},
		{"missing open paren", "1234 claude) S 567\n", 0, false},
		{"reversed parens", "1234 ) ( S 567\n", 0, false},
		{"too few fields", "1234 (claude) S\n", 0, false},
		{"multi-char state", "1234 (claude) RUN 567\n", 0, false},
		{"non-numeric ppid", "1234 (claude) S nope\n", 0, false},
	} {
		got, ok := parseLinuxStatPPID([]byte(tc.in))
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: parseLinuxStatPPID = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}

	dir := t.TempDir()
	exactPath := filepath.Join(dir, "exact.stat")
	prefix := "1234 (claude) S 567 "
	exact := prefix + strings.Repeat("0", maxStatBytes-len(prefix))
	if err := os.WriteFile(exactPath, []byte(exact), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readLinuxStatPPID(exactPath); !ok || got != 567 {
		t.Errorf("exact maxStatBytes = (%d, %v), want (567, true)", got, ok)
	}

	// Oversized file (even with a misleading ')' in the first 8 KiB) must be rejected, not truncated.
	overPath := filepath.Join(dir, "over.stat")
	over := "1234 (fake) S 999 " + strings.Repeat("a", maxStatBytes) + ") R 567\n"
	if err := os.WriteFile(overPath, []byte(over), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readLinuxStatPPID(overPath); ok || got != 0 {
		t.Errorf("oversized stat = (%d, %v), want (0, false)", got, ok)
	}
	if got, ok := readLinuxStatPPID(filepath.Join(dir, "missing.stat")); ok || got != 0 {
		t.Errorf("missing stat = (%d, %v), want (0, false)", got, ok)
	}
}

func TestParentNewerThanChild(t *testing.T) {
	times := map[int]uint64{
		10: 1000, // grandparent
		20: 2000, // parent (older than child 30)
		30: 3000, // child
		40: 3000, // same timestamp as child 30
		50: 4000, // reused PID, strictly newer than child 30
		60: 0,    // zero creation time
	}
	lookup := func(pid int) (uint64, bool) {
		v, ok := times[pid]
		return v, ok
	}
	for _, tc := range []struct {
		name          string
		child, parent int
		want          bool
	}{
		{"real older parent continues", 30, 20, false},
		{"equal timestamp continues", 30, 40, false},
		{"strictly newer parent stops", 30, 50, true},
		{"unreadable parent continues", 30, 999, false},
		{"unreadable child continues", 999, 50, false},
		{"zero parent time continues", 30, 60, false},
		{"zero child time continues", 60, 50, false},
	} {
		if got := parentNewerThanChild(tc.child, tc.parent, lookup); got != tc.want {
			t.Errorf("%s: parentNewerThanChild(%d, %d) = %v, want %v", tc.name, tc.child, tc.parent, got, tc.want)
		}
	}
}
