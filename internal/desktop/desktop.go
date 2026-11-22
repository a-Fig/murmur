// Package desktop is Murmur's desktop app (#23): a native window around the page `mur ui` opens
// in a browser, so the two stay the same page. It adds only what a window can do: its own icon in
// the taskbar or Dock, an unread count on it, and a notification when a message comes to the
// person while they are looking elsewhere.
//
// The window is the system's web view (WebView2, WKWebView, WebKitGTK) through glaze, which
// loads it at run time with purego. There is no cgo, so `mur` still cross-compiles for every
// platform from one Linux runner, and a machine without a web view still runs every other
// command. docs/design/desktop.md has the reasons and the measurements.
package desktop

import (
	"context"
	"net/url"
	"os/exec"
	"strings"
)

// Options is what Run needs from the command.
type Options struct {
	// Page is the address of the clone's page, with its token in the fragment.
	Page string
	// LightFrame sets the title bar before navigation. The page updates it once loaded.
	LightFrame bool
	// Follow is called every few seconds with the host:port the page last said it was served
	// from, and returns the address the window should show: the same page, or that clone's page
	// again after its daemon came back on another port. "" leaves the window as it is.
	Follow func(ctx context.Context, host string) string
	// Open opens an address in the person's browser: links to GitHub and the like, which have no
	// place in the app's window.
	Open func(url string) error
	// Debug turns on the web view's developer tools.
	Debug bool
	// Instance is the window's claim to be the one window (Claim), so a later `mur app` brings it
	// forward rather than opening another (BringForward). Nil claims nothing.
	Instance *Instance
	// Switch is given the repo a later `mur app` asked for and returns the address of its page,
	// or "" to leave the window where it is.
	Switch func(ctx context.Context, gitDir string) string
}

func hostOf(page string) string {
	u, err := url.Parse(page)
	if err != nil {
		return ""
	}
	return u.Host
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// waited is the end of cmd's process, once it comes.
func waited(cmd *exec.Cmd) <-chan error {
	c := make(chan error, 1)
	go func() { c <- cmd.Wait() }()
	return c
}

// appCommand says whether the arguments after the program's name run `mur app`, after any -C DIR
// that comes before the command (cmd/mur's leadingDirs).
func appCommand(args []string) bool {
	for len(args) > 0 {
		name, _, hasValue := strings.Cut(args[0], "=")
		if name != "-C" && name != "--C" {
			break
		}
		if hasValue {
			args = args[1:]
		} else {
			args = args[min(2, len(args)):]
		}
	}
	return len(args) > 0 && args[0] == "app"
}

// native is what each platform adds to the window.
type native interface {
	// badge shows n unread on the app's icon, or nothing for 0. On the UI thread.
	badge(n int)
	// frame makes the window's title bar dark or light, to match the page. On the UI thread.
	frame(dark bool)
	// inFront says whether the window is in front of the person, so a message needs no
	// notification; seen is the page's guess, for a platform that can't tell. On the UI thread.
	inFront(seen bool) bool
	// notify shows a notification. Any goroutine.
	notify(title, body string) error
	close()
}
