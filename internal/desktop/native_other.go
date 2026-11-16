//go:build linux && desktop

package desktop

import (
	"os/exec"

	"github.com/crgimenes/glaze"

	"github.com/a-Fig/murmur/internal/brand"
)

// On Linux the window's title carries the unread count, "(3) Murmur", as a browser tab's does:
// a count on a launcher's icon, or a tray icon, is a D-Bus service each desktop does its own way.
// A notification is notify-send's, from libnotify, where it is installed.

type linuxNative struct{}

func beforeWindow() (after func()) { return func() {} }

// systemTellsFront: Linux desktops share no way to say which window is in front, so the page
// isn't told, and goes by its own document.hidden (#638).
const systemTellsFront = false

func newNative(glaze.WebView) native { return linuxNative{} }

func (linuxNative) badge(int) {}

func (linuxNative) frame(bool) {} // the desktop's theme decides

func (linuxNative) inFront(seen bool) bool { return seen }

func (linuxNative) notify(title, body string) error {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return err
	}
	return exec.Command(path, "--app-name="+brand.Product, "--", title, body).Run()
}

func (linuxNative) close() {}
