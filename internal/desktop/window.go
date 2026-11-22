//go:build windows || darwin || (linux && desktop)

package desktop

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/crgimenes/glaze"

	"github.com/a-Fig/murmur/internal/brand"
)

var (
	//go:embed icons/icon.png
	iconPNG []byte // 256 px, the full tile: the window and taskbar icon
	//go:embed icons/small.png
	smallPNG []byte // 32 px: the tray, and a window's small icon
	//go:embed icons/dock.png
	dockPNG []byte // 512 px, inset as macOS draws app icons: the Dock
)

// followEvery is how often the window checks that its daemon is still where the page is.
const followEvery = 10 * time.Second

// frontEvery is how often the window checks whether it is in front, to tell the page (#638).
const frontEvery = time.Second

// Supported says whether this build has the window.
const Supported = true

// Run shows the window and returns when it is closed. It must be called from the main goroutine:
// every platform's UI belongs to the thread that made it.
func Run(ctx context.Context, opts Options) error {
	after := beforeWindow()
	var a *app
	w, err := glaze.NewWithOptions(glaze.Options{
		Debug:             opts.Debug,
		HideUntilLoaded:   true,
		FrameAutosaveName: brand.Product,
		AcceptsFirstMouse: true,
		OnClose: func() {
			opts.Instance.closingDown() // before it has gone, so no click is answered by it
			a.w.Terminate()
		},
		// A file the page saves (#570) goes where a save panel says. Without this the web view
		// cancels every download on macOS and Linux; on Windows it handles them itself.
		OnDownload: func(name string) string {
			path, err := a.w.SaveFile(glaze.FileDialogOptions{Filename: name})
			if err != nil {
				return ""
			}
			return path
		},
	})
	after()
	if err != nil {
		return err
	}
	defer w.Destroy()
	a = &app{w: w, open: opts.Open, host: hostOf(opts.Page)}
	w.SetTitle(brand.Product)
	w.SetSize(1280, 820, glaze.HintNone)
	a.native = newNative(w)
	defer a.native.close()

	if err := w.Bind("murmurApp", a.fromPage); err != nil {
		return err
	}
	if err := w.Bind("murmurOpen", a.openOutside); err != nil {
		return err
	}
	if err := w.Bind("murmurHere", a.here); err != nil {
		return err
	}
	w.Init(initScript)
	w.Navigate(opts.Page)

	// Ctrl-C on `mur app --wait` closes the window, as it would end any other command.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			opts.Instance.closingDown()
			w.Terminate()
		case <-done:
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Instance != nil {
		in := opts.Instance
		in.onShow(func(gitDir string, reply func(bool)) { a.show(ctx, gitDir, opts.Switch, in.isClosing, reply) })
		defer opts.Instance.closingDown() // before the web view is destroyed
	}
	if opts.Follow != nil {
		go a.follow(ctx, opts.Follow)
	}
	if systemTellsFront {
		go a.watchFront(ctx)
	}
	w.Run()
	return nil
}

type app struct {
	w      glaze.WebView
	native native
	open   func(string) error

	mu     sync.Mutex
	host   string // where the page last said it was, or where it was sent
	unread int
	dark   *bool // the frame's theme, once the page has said
	front  *bool // what the page was last told of whether the window is in front; nil, not yet
}

// show brings the window forward, asked by a later `mur app`, and moves it to the repo at gitDir
// when that asked for one, and then says whether it came forward. It does both on the UI thread,
// so a close queued there first is seen, and the one asking opens a window of its own rather than
// being told this one came. Any goroutine.
func (a *app) show(ctx context.Context, gitDir string, to func(context.Context, string) string, closing func() bool, reply func(bool)) {
	a.w.Dispatch(func() {
		if ctx.Err() != nil || closing() {
			reply(false)
			return
		}
		a.raise()
		reply(true)
		if gitDir != "" && to != nil {
			a.switchTo(ctx, gitDir, to)
		}
	})
}

// switchTo moves the window to the repo at gitDir, once its daemon is up. Any goroutine.
func (a *app) switchTo(ctx context.Context, gitDir string, to func(context.Context, string) string) {
	go func() {
		page := to(ctx, gitDir)
		if page == "" || ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		a.host = hostOf(page)
		a.mu.Unlock()
		a.w.Dispatch(func() { a.w.Navigate(page) })
	}()
}

// raise brings the window forward. On the UI thread.
func (a *app) raise() {
	if s, ok := a.native.(interface{ show() }); ok {
		s.show() // Windows': restores a minimized window, and leaves a maximized one maximized
		return
	}
	a.w.Raise()
}

// pageNote is what the page tells the window each time its title or unread count changes, and
// when messages to the person arrive (app.js, tellApp).
type pageNote struct {
	Host   string `json:"host"`
	Title  string `json:"title"`
	Unread int    `json:"unread"`
	Notes  []note `json:"notes"`
	// Seen is the page's own guess that someone is looking at it, for a platform whose window
	// can't say: a web view keeps its focus when its window is minimized.
	Seen bool `json:"seen"`
	// Dark says the page's ground is dark, for the window's frame.
	Dark bool `json:"dark"`
}

type note struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// fromPage takes a pageNote. Any page in the window can call it, so it does nothing a page
// shouldn't: it sets the window's title and count, shows a notification, and records a host that
// follow then looks up among this machine's clones.
func (a *app) fromPage(n pageNote) {
	n.Title = clip(n.Title, 200)
	if n.Unread < 0 {
		n.Unread = 0
	}
	a.mu.Lock()
	if n.Host != "" {
		a.host = n.Host
	}
	changed := n.Unread != a.unread
	a.unread = n.Unread
	themed := a.dark == nil || *a.dark != n.Dark
	a.dark = &n.Dark
	a.mu.Unlock()
	a.w.Dispatch(func() {
		if n.Title != "" {
			a.w.SetTitle(n.Title)
		}
		if changed {
			a.native.badge(n.Unread)
		}
		if themed {
			a.native.frame(n.Dark)
		}
		if len(n.Notes) == 0 || a.native.inFront(n.Seen) {
			return
		}
		notes := n.Notes[:min(len(n.Notes), 3)] // a burst is the page's to sum up
		go func() {
			for _, m := range notes {
				if err := a.native.notify(clip(m.Title, 100), clip(m.Body, 300)); err != nil {
					log.Printf("notification: %v", err)
				}
			}
		}()
	})
}

// here takes the host of each document the window loads, whatever page it is: follow then sends a
// window that ended up on another page (a file dropped on it, a link opened in place) back to the
// clone's page. A page's own host can't take the window anywhere but a joined clone's page.
func (a *app) here(host string) {
	a.mu.Lock()
	a.host = host
	a.front = nil // a new document, which hasn't been told
	a.mu.Unlock()
}

// watchFront tells the page whether the window is in front, each time that changes. The page can't
// tell: under another window its document stays visible, and a web view's focus isn't to be trusted
// (docs/design/desktop.md). While the window is behind, the page marks its calls hidden, so an app
// left open under the editor doesn't keep the person active or hold off an update's restart (#638).
func (a *app) watchFront(ctx context.Context) {
	t := time.NewTicker(frontEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.w.Dispatch(a.tellFront)
	}
}

// tellFront tells the page whether the window is in front, if that isn't what it was last told.
// On the UI thread.
func (a *app) tellFront() {
	front := a.native.inFront(true)
	a.mu.Lock()
	same := a.front != nil && *a.front == front
	a.front = &front
	a.mu.Unlock()
	if !same {
		a.w.Eval(fmt.Sprintf("window.murmurFront && window.murmurFront(%t)", front))
	}
}

// openOutside opens a link the page would have opened in a new tab, in the person's browser.
func (a *app) openOutside(link string) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return
	}
	if err := a.open(u.String()); err != nil {
		log.Printf("open %s: %v", u.Redacted(), err)
	}
}

// follow keeps the window on a live page. The app is a person waiting for messages, so its page
// marks every call as the app's (X-Page-App), and the daemon stays up while the window is open,
// minimized or not. But a daemon can still go: a crash, `mur stop`, a reboot of what it ran on.
// Its next start is on a new port with a new token, which the old page can't reach, so follow
// starts it again and sends the window there.
func (a *app) follow(ctx context.Context, follow func(context.Context, string) string) {
	t := time.NewTicker(followEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		host := a.host
		a.mu.Unlock()
		page := follow(ctx, host)
		if page == "" || ctx.Err() != nil {
			continue
		}
		a.mu.Lock()
		a.host = hostOf(page)
		a.mu.Unlock()
		a.w.Dispatch(func() { a.w.Navigate(page) })
	}
}

// initScript runs at the start of every document in the window. A link to another site, which
// the page opens in a new tab, opens in the person's browser instead: the window is for the page,
// and a page from elsewhere in it would be handed the window's bindings.
const initScript = `(() => {
  const outside = (href) => {
    try {
      const u = new URL(href, location.href);
      return (u.protocol === "http:" || u.protocol === "https:") && u.origin !== location.origin ? u.href : "";
    } catch { return ""; }
  };
  const click = (e) => {
    const a = e.target instanceof Element && e.target.closest("a[href]");
    const href = a && outside(a.href);
    if (!href) return;
    e.preventDefault();
    window.murmurOpen(href);
  };
  addEventListener("DOMContentLoaded", () => window.murmurHere(location.host));
  addEventListener("click", click, true);
  addEventListener("auxclick", click, true);
  const open = window.open;
  window.open = (href, ...rest) => {
    const out = outside(href);
    if (out) { window.murmurOpen(out); return null; }
    return open.call(window, href, ...rest);
  };
})();`
