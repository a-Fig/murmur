package desktop

import (
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/crgimenes/glaze"
	"github.com/ebitengine/purego/objc"
)

// On macOS the Dock shows the app's icon and its unread count, as a badge on the icon. A
// notification goes through osascript: a binary outside an app bundle has no bundle identifier,
// which UNUserNotificationCenter needs, so the banner is Script Editor's, with its icon, and macOS
// asks once whether Script Editor may show them. An app bundle would make them Murmur's.

// AppKit works only on the process's first thread. Go starts the main goroutine there, and this
// keeps it there before anything can move it, for `mur app` alone: no other command needs it.
func init() {
	if appCommand(os.Args[1:]) || FromBundle() {
		runtime.LockOSThread()
	}
}

type macNative struct {
	win objc.ID // the app's window, an NSWindow
}

func beforeWindow() (after func()) { return func() {} }

// systemTellsFront: macOS says whether Murmur is the active app, so the page is told (#638).
const systemTellsFront = true

func newNative(w glaze.WebView) native {
	if err := glaze.SetAppIcon(dockPNG); err != nil {
		log.Printf("Dock icon: %v", err)
	}
	return macNative{win: objc.ID(uintptr(w.Window()))}
}

// badge is on the UI thread, which is the main thread AppKit wants.
func (macNative) badge(n int) {
	label := ""
	if n > 0 {
		label = strconv.Itoa(n)
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	tile := app.Send(objc.RegisterName("dockTile"))
	s := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), label)
	tile.Send(objc.RegisterName("setBadgeLabel:"), s)
}

// frame sets the app's appearance, which its window's title bar follows.
func (macNative) frame(dark bool) {
	name := "NSAppearanceNameAqua"
	if dark {
		name = "NSAppearanceNameDarkAqua"
	}
	s := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), name)
	look := objc.ID(objc.GetClass("NSAppearance")).Send(objc.RegisterName("appearanceNamed:"), s)
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	app.Send(objc.RegisterName("setAppearance:"), look)
}

// inFront is whether Murmur is the active app, as when its window was clicked last, with its
// window on the screen. Being active isn't enough: after Cmd-M an app often stays active with its
// window in the Dock (#711), and after a swipe to another Space with its window there. The
// window's occlusion state would say both at once, but over ssh it never changed, so it is taken
// on trust nowhere (2026-11-16). On the UI thread.
func (n macNative) inFront(bool) bool {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	if !objc.Send[bool](app, objc.RegisterName("isActive")) || n.win == 0 {
		return false
	}
	is := func(sel string) bool { return objc.Send[bool](n.win, objc.RegisterName(sel)) }
	return is("isVisible") && !is("isMiniaturized") && is("isOnActiveSpace")
}

// notify passes the title and body as arguments, so nothing in them is read as AppleScript.
func (macNative) notify(title, body string) error {
	return exec.Command("/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body).Run()
}

func (macNative) close() {}
