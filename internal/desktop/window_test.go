//go:build windows || darwin || (linux && desktop)

package desktop

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/crgimenes/glaze"
)

// fakeView stands in for the web view: Dispatch runs at once, as if on the UI thread, and the
// calls the app makes are recorded. Any other method would panic, through the nil interface.
type fakeView struct {
	glaze.WebView
	mu       sync.Mutex
	titles   []string
	navigate []string
	evals    []string
}

func (v *fakeView) Eval(js string) { v.mu.Lock(); v.evals = append(v.evals, js); v.mu.Unlock() }

func (v *fakeView) Dispatch(f func()) { f() }
func (v *fakeView) SetTitle(t string) { v.mu.Lock(); v.titles = append(v.titles, t); v.mu.Unlock() }
func (v *fakeView) Navigate(page string) {
	v.mu.Lock()
	v.navigate = append(v.navigate, page)
	v.mu.Unlock()
}

type fakeNative struct {
	front  bool
	badges []int
	frames []bool
	notes  chan [2]string
}

func (n *fakeNative) badge(c int)       { n.badges = append(n.badges, c) }
func (n *fakeNative) frame(dark bool)   { n.frames = append(n.frames, dark) }
func (n *fakeNative) inFront(bool) bool { return n.front }
func (n *fakeNative) close()            {}
func (n *fakeNative) notify(t, b string) error {
	n.notes <- [2]string{t, b}
	return nil
}

func TestFromPage(t *testing.T) {
	v := &fakeView{}
	n := &fakeNative{notes: make(chan [2]string, 10)}
	a := &app{w: v, native: n}

	a.fromPage(pageNote{Host: "127.0.0.1:9", Title: "Murmur", Dark: true, Seen: true})
	a.fromPage(pageNote{Host: "127.0.0.1:9", Title: "(2) Murmur", Unread: 2, Dark: true})
	a.fromPage(pageNote{Host: "127.0.0.1:9", Title: "(2) Murmur", Unread: 2, Dark: false})
	if got := strings.Join(v.titles, ","); got != "Murmur,(2) Murmur,(2) Murmur" {
		t.Errorf("titles %s", got)
	}
	if len(n.badges) != 1 || n.badges[0] != 2 {
		t.Errorf("badges %v, want one, of 2, when the count changed", n.badges)
	}
	if len(n.frames) != 2 || !n.frames[0] || n.frames[1] {
		t.Errorf("frames %v, want dark then light", n.frames)
	}
	if a.host != "127.0.0.1:9" {
		t.Errorf("host %q", a.host)
	}

	// In front: no notification.
	n.front = true
	a.fromPage(pageNote{Unread: 3, Notes: []note{{"quiet-otter", "Re: the demo"}}})
	// Behind: up to three, cut to fit.
	n.front = false
	long := strings.Repeat("x", 400)
	a.fromPage(pageNote{Unread: 7, Notes: []note{{"a", long}, {"b", "2"}, {"c", "3"}, {"d", "4"}}})
	var got [][2]string
	for range 3 {
		got = append(got, <-n.notes)
	}
	select {
	case extra := <-n.notes:
		t.Fatalf("a fourth notification, or one while in front: %v", extra)
	default:
	}
	if got[0][0] != "a" || len([]rune(got[0][1])) != 300 || got[2][0] != "c" {
		t.Errorf("notifications %q", got)
	}
}

func TestOpenOutsideTakesOnlyWebLinks(t *testing.T) {
	var opened []string
	a := &app{open: func(u string) error { opened = append(opened, u); return nil }}
	for _, link := range []string{"https://github.com/a-Fig/murmur/pull/1", "file:///etc/passwd", "javascript:alert(1)", "ms-settings:", "http://example.com/x"} {
		a.openOutside(link)
	}
	if strings.Join(opened, " ") != "https://github.com/a-Fig/murmur/pull/1 http://example.com/x" {
		t.Fatalf("opened %v", opened)
	}
}

func TestClip(t *testing.T) {
	if got := clip("  héllo wörld  ", 5); got != "héll…" {
		t.Fatalf("clip: %q", got)
	}
	if got := clip("ok", 5); got != "ok" {
		t.Fatalf("clip: %q", got)
	}
}

// The window tells its page whether it is in front each time that changes, and tells a new document
// again, which hasn't been told (#638).
func TestTheWindowTellsThePageWhetherItIsInFront(t *testing.T) {
	v := &fakeView{}
	n := &fakeNative{front: true}
	a := &app{w: v, native: n}
	a.tellFront()
	a.tellFront()
	n.front = false
	a.tellFront()
	a.tellFront()
	a.here("127.0.0.1:9")
	a.tellFront()
	want := []string{
		"window.murmurFront && window.murmurFront(true)",
		"window.murmurFront && window.murmurFront(false)",
		"window.murmurFront && window.murmurFront(false)",
	}
	if !slices.Equal(v.evals, want) {
		t.Errorf("told the page %q, want %q", v.evals, want)
	}
}
