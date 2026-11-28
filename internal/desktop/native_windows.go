package desktop

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/crgimenes/glaze"
	"golang.org/x/sys/windows"

	"github.com/a-Fig/murmur/internal/brand"
)

// On Windows the window gets the app's icon (a Go binary carries none), an icon in the
// notification area, and a dot on its taskbar button while something is unread. A notification
// is a balloon from that icon, which Windows 10 and 11 show as a toast with the app's icon. The
// Start menu shortcut (#616) registers the AppUserModelID a toast of its own needs; the balloon is
// still what shows.

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	ole32   = windows.NewLazySystemDLL("ole32.dll")

	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procCoCreateInstance         = ole32.NewProc("CoCreateInstance")
	procDwmSetWindowAttribute    = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

const (
	wmSetIcon  = 0x0080
	wmApp      = 0x8000
	trayMsg    = wmApp + 1
	iconSmall  = 0
	iconBig    = 1
	swRestore  = 9
	wmLButtonU = 0x0202

	ninSelect           = 0x0400
	ninKeySelect        = 0x0401
	ninBalloonUserClick = 0x0405

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4

	nifMessage  = 0x01
	nifIcon     = 0x02
	nifTip      = 0x04
	nifInfo     = 0x10
	nifShowTip  = 0x80
	niifUser    = 0x04
	niifLarge   = 0x20
	notifyIconV = 4
)

// notifyIconData is NOTIFYICONDATAW as shell32 has it since Vista.
type notifyIconData struct {
	cbSize           uint32
	hWnd             windows.HWND
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

type winNative struct {
	w     glaze.WebView
	hwnd  windows.HWND
	big   windows.Handle
	small windows.Handle
	dot   windows.Handle

	// The messages Explorer sends when it starts again, and when the taskbar button is made: the
	// tray icon and the dot are lost with the old taskbar.
	taskbarCreated uintptr
	buttonCreated  uintptr
	oldProc        uintptr

	taskbar unsafe.Pointer // ITaskbarList3, or nil

	mu     sync.Mutex // the tray icon is changed from the UI thread and from notify
	unread int
	tray   bool
}

// beforeWindow runs WebView2's GPU work in its browser process rather than a process of its own:
// about 120 MB less private memory at idle (measured 2026-11-15, docs/design/desktop.md), and a page
// of text and a few images needs no isolated GPU. Compositing is done in software with it: with GPU
// compositing in the browser process, the right-click menu never reaches the screen (#1272), though
// it opens and takes keys. Someone who sets the variable themselves is left to it. The runtime
// reads it as the web view is made; what it returns puts it back, so the daemons and browsers the
// app starts don't inherit it.
func beforeWindow() (after func()) {
	setProcessAppID() // the Start menu shortcut's (launcher_windows.go)
	const env = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
	if _, set := os.LookupEnv(env); set {
		return func() {}
	}
	os.Setenv(env, "--in-process-gpu --disable-gpu-compositing")
	return func() { os.Unsetenv(env) }
}

// The window procedure is installed once per process, and its callback can't be freed.
var current *winNative

// systemTellsFront: Windows says which window is in front, so the page is told (#638).
const systemTellsFront = true

func newNative(w glaze.WebView) native {
	n := &winNative{w: w, hwnd: windows.HWND(w.Window())}
	n.big = iconFrom(iconPNG)
	n.small = iconFrom(smallPNG)
	n.dot = iconFrom(dotPNG())
	if n.big != 0 {
		procSendMessageW.Call(uintptr(n.hwnd), wmSetIcon, iconBig, uintptr(n.big))
	}
	if n.small != 0 {
		procSendMessageW.Call(uintptr(n.hwnd), wmSetIcon, iconSmall, uintptr(n.small))
	}
	n.taskbarCreated = registerMessage("TaskbarCreated")
	n.buttonCreated = registerMessage("TaskbarButtonCreated")
	current = n
	gwlpWndProc := -4
	n.oldProc, _, _ = procSetWindowLongPtrW.Call(uintptr(n.hwnd), uintptr(gwlpWndProc), syscall.NewCallback(wndProc))
	n.taskbar = newTaskbarList()
	n.addTray()
	return n
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	n := current
	switch {
	case msg == trayMsg:
		switch lp & 0xffff {
		case wmLButtonU, ninSelect, ninKeySelect, ninBalloonUserClick:
			n.show()
		}
		return 0
	case msg == n.taskbarCreated && msg != 0:
		n.mu.Lock()
		n.tray = false
		n.mu.Unlock()
		n.addTray()
	case msg == n.buttonCreated && msg != 0:
		n.overlay()
	}
	r, _, _ := procCallWindowProcW.Call(n.oldProc, hwnd, msg, wp, lp)
	return r
}

func (n *winNative) show() {
	if r, _, _ := procIsIconic.Call(uintptr(n.hwnd)); r != 0 {
		procShowWindow.Call(uintptr(n.hwnd), swRestore)
	}
	n.w.Raise()
}

// frame asks for a dark title bar, which Windows 10 (20H1) and 11 draw; earlier ones ignore it.
func (n *winNative) frame(dark bool) {
	const dwmwaUseImmersiveDarkMode = 20
	var on int32
	if dark {
		on = 1
	}
	procDwmSetWindowAttribute.Call(uintptr(n.hwnd), dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), 4)
}

// inFront is whether the window is the one in front, and not minimized.
func (n *winNative) inFront(bool) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	iconic, _, _ := procIsIconic.Call(uintptr(n.hwnd))
	return windows.HWND(fg) == n.hwnd && iconic == 0
}

func (n *winNative) data() notifyIconData {
	d := notifyIconData{hWnd: n.hwnd, uID: 1}
	d.cbSize = uint32(unsafe.Sizeof(d))
	return d
}

// addTray puts the icon in the notification area. No lock is held across a shell call: Explorer
// may send the window a message meanwhile, which runs wndProc on this thread.
func (n *winNative) addTray() {
	d := n.data()
	d.uFlags = nifMessage | nifIcon | nifTip | nifShowTip
	d.uCallbackMessage = trayMsg
	d.hIcon = n.small
	n.mu.Lock()
	copyUTF16(d.szTip[:], tip(n.unread))
	n.mu.Unlock()
	if r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&d))); r == 0 {
		log.Print("the notification area refused the icon")
		return
	}
	d.uVersion = notifyIconV
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&d)))
	n.mu.Lock()
	n.tray = true
	n.mu.Unlock()
}

func tip(unread int) string {
	if unread == 0 {
		return brand.Product
	}
	return brand.Product + ": " + strconv.Itoa(unread) + " unread"
}

func (n *winNative) badge(unread int) {
	n.mu.Lock()
	n.unread = unread
	tray := n.tray
	n.mu.Unlock()
	if tray {
		d := n.data()
		d.uFlags = nifTip | nifShowTip
		copyUTF16(d.szTip[:], tip(unread))
		procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d)))
	}
	n.overlay()
}

// overlay puts the dot on the taskbar button, or takes it off. On the UI thread, where the
// taskbar list was made.
func (n *winNative) overlay() {
	if n.taskbar == nil {
		return
	}
	n.mu.Lock()
	unread := n.unread
	n.mu.Unlock()
	icon, desc := uintptr(0), ""
	if unread > 0 {
		icon, desc = uintptr(n.dot), strconv.Itoa(unread)+" unread"
	}
	p, _ := windows.UTF16PtrFromString(desc)
	vtbl := *(**[19]uintptr)(n.taskbar)
	syscall.SyscallN(vtbl[18], uintptr(n.taskbar), uintptr(n.hwnd), icon, uintptr(unsafe.Pointer(p))) // SetOverlayIcon
}

func (n *winNative) notify(title, body string) error {
	n.mu.Lock()
	tray := n.tray
	n.mu.Unlock()
	if !tray {
		return fmt.Errorf("no icon in the notification area to show it from")
	}
	d := n.data()
	d.uFlags = nifInfo
	copyUTF16(d.szInfoTitle[:], title)
	copyUTF16(d.szInfo[:], body)
	d.dwInfoFlags = niifUser | niifLarge
	d.hBalloonIcon = n.big
	if r, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d))); r == 0 {
		return fmt.Errorf("the notification area refused it")
	}
	return nil
}

func (n *winNative) close() {
	n.mu.Lock()
	tray := n.tray
	n.tray = false
	n.mu.Unlock()
	if tray {
		d := n.data()
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&d)))
	}
	if n.taskbar != nil {
		vtbl := *(**[3]uintptr)(n.taskbar)
		syscall.SyscallN(vtbl[2], uintptr(n.taskbar)) // Release
		n.taskbar = nil
	}
}

// iconFrom makes an icon of a PNG, which Windows has taken as an icon's image since Vista.
func iconFrom(png []byte) windows.Handle {
	const lrDefaultColor = 0
	h, _, err := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000, 0, 0, lrDefaultColor)
	if h == 0 {
		log.Printf("icon: %v", err)
	}
	return windows.Handle(h)
}

func registerMessage(name string) uintptr {
	p, _ := windows.UTF16PtrFromString(name)
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	return r
}

// newTaskbarList makes the shell's ITaskbarList3, which puts the dot on the taskbar button. The
// web view has set up COM on this thread already.
func newTaskbarList() unsafe.Pointer {
	clsid, _ := windows.GUIDFromString("{56FDF344-FD6D-11d0-958A-006097C9A090}")
	iid, _ := windows.GUIDFromString("{EA1AFB91-9E28-4B86-90E9-9E9F8A5EEFAF}")
	const clsctxInprocServer = 1
	var p unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&p)))
	if int32(hr) < 0 || p == nil {
		log.Printf("taskbar list: HRESULT 0x%08X", uint32(hr))
		return nil
	}
	vtbl := *(**[4]uintptr)(p)
	syscall.SyscallN(vtbl[3], uintptr(p)) // HrInit
	return p
}

// copyUTF16 copies s into a fixed field, cut to fit with room for its terminating zero.
func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// dotPNG draws the taskbar button's dot: the page's blue in a cream ring, so it shows on the blue
// tile it sits on.
func dotPNG() []byte {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	blue := color.NRGBA{0x22, 0x5B, 0xFF, 0xFF}
	cream := color.NRGBA{0xFF, 0xFB, 0xEA, 0xFF}
	c := float64(size) / 2
	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)+0.5-c, float64(y)+0.5-c)
			px := blue
			if d > c-5 {
				px = cream
			}
			// Edges get a pixel of fade, so the dot is round at 16 px too.
			if a := c - d; a < 1 {
				px.A = uint8(max(0, a) * 255)
			}
			img.SetNRGBA(x, y, px)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}
