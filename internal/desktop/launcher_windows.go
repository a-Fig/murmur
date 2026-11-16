package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/a-Fig/murmur/internal/brand"
)

// On Windows the launcher is a shortcut in the user's Start menu. It runs mur through
// `conhost.exe --headless`: mur is a console program, and a shortcut straight to it would open a
// console window for as long as `mur app` takes to start the window. The shortcut carries the
// app's AppUserModelID, which the window's process takes too (beforeWindow), so the window's
// taskbar button is the shortcut's: pinned, it starts the app.

// launcherPath is the shortcut's path. The Start menu is the Programs folder under APPDATA: where
// it is unless folder redirection moved it, and what a test can point elsewhere, which the shell's
// known folder (FOLDERID_Programs) doesn't follow.
func launcherPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", brand.Product+".lnk")
}

func installLauncher(exe, dir, where string) error {
	ico := filepath.Join(dir, brand.Product+".ico")
	if err := os.WriteFile(ico, icoFrom(iconPNG, smallPNG), 0o644); err != nil {
		return err
	}
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	if os.Getenv("SystemRoot") == "" || !exists(conhost) {
		return errors.New("no conhost.exe in " + filepath.Dir(conhost))
	}
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return err
	}
	return writeShortcut(where, shortcut{
		target: conhost,
		args:   "--headless " + windows.EscapeArg(exe) + " app --from-launcher",
		dir:    filepath.Dir(exe),
		icon:   ico,
		desc:   "Messages between the coding agents in your repos",
		appID:  brand.AppID,
	})
}

// ownsLauncher says a shortcut no record of ours names is Murmur's: on Windows that would mean
// reading it back through COM, so a stranger's Murmur.lnk is never taken for ours.
func ownsLauncher(string) bool { return false }

// removeLauncherFiles takes out the icon the shortcut used.
func removeLauncherFiles(dir string) { os.Remove(filepath.Join(dir, brand.Product+".ico")) }

type shortcut struct {
	target, args, dir, icon, desc, appID string
}

var (
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procSetProcessAppID  = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procAllowForeground  = user32.NewProc("AllowSetForegroundWindow")
	clsidShellLink       = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW        = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile       = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPropertyStore     = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	pkeyAppUserModelID   = propertyKey{fmtid: windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}, pid: 5}
	errShortcutInterface = errors.New("the shell's shortcut object is missing an interface")
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT holding a string (VT_LPWSTR): 24 bytes on 64-bit Windows.
type propVariant struct {
	vt       uint16
	reserved [3]uint16
	val      uintptr
	pad      uintptr
}

const (
	clsctxInproc            = 0x1
	coinitApartmentThreaded = 0x2
	vtLPWSTR                = 31
	swShowMinNoActive       = 7
)

// call calls method i of the COM object at obj, and turns a failed HRESULT into an error. Every
// pointer in args must be to memory the garbage collector neither frees nor moves until the call
// returns: heap memory the caller keeps alive (see writeShortcut), never a variable on the stack,
// which moves when the stack grows.
func call(obj unsafe.Pointer, i int, name string, args ...uintptr) error {
	vtbl := *(**[32]uintptr)(obj)
	hr, _, _ := syscall.SyscallN(vtbl[i], append([]uintptr{uintptr(obj)}, args...)...)
	if int32(hr) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08X", name, uint32(hr))
	}
	return nil
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		call(obj, 2, "Release")
	}
}

// writeShortcut writes a .lnk through the shell's ShellLink object (IShellLinkW), with the
// AppUserModelID set through its property store, as Windows' own documentation does it.
//
// What COM is given a pointer to is made with new or as a string's UTF-16 copy, both on the heap,
// where the garbage collector doesn't move them, and kept alive in keep until the end: a pointer
// held only as a uintptr is no reference, and the memory under it could be freed (Opus review).
func writeShortcut(path string, s shortcut) error {
	runtime.LockOSThread() // COM's apartment is the thread's
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if int32(hr) >= 0 {
		defer procCoUninitialize.Call()
	}
	var keep []any
	defer func() { runtime.KeepAlive(keep) }()
	str := func(v string) uintptr {
		p, _ := windows.UTF16PtrFromString(v)
		keep = append(keep, p)
		return uintptr(unsafe.Pointer(p))
	}
	out := func() *unsafe.Pointer { // where COM writes an interface pointer
		p := new(unsafe.Pointer)
		keep = append(keep, p)
		return p
	}

	link := out()
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInproc, uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(link)))
	if int32(hr) < 0 || *link == nil {
		return fmt.Errorf("making a shortcut: HRESULT 0x%08X", uint32(hr))
	}
	defer release(*link)
	// IShellLinkW: SetPath 20, SetArguments 11, SetWorkingDirectory 9, SetDescription 7,
	// SetIconLocation 17, SetShowCmd 15.
	steps := []struct {
		i    int
		name string
		args []uintptr
	}{
		{20, "SetPath", []uintptr{str(s.target)}},
		{11, "SetArguments", []uintptr{str(s.args)}},
		{9, "SetWorkingDirectory", []uintptr{str(s.dir)}},
		{7, "SetDescription", []uintptr{str(s.desc)}},
		{17, "SetIconLocation", []uintptr{str(s.icon), 0}},
		// conhost --headless shows nothing; minimized, nothing shows should a Windows not know it.
		{15, "SetShowCmd", []uintptr{swShowMinNoActive}},
	}
	for _, st := range steps {
		if err := call(*link, st.i, st.name, st.args...); err != nil {
			return err
		}
	}

	store := out()
	if err := call(*link, 0, "QueryInterface", uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(store))); err != nil || *store == nil {
		return errShortcutInterface
	}
	defer release(*store)
	v := new(propVariant)
	keep = append(keep, v)
	v.vt, v.val = vtLPWSTR, str(s.appID)
	if err := call(*store, 6, "SetValue", uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(v))); err != nil {
		return err
	}
	if err := call(*store, 7, "Commit"); err != nil {
		return err
	}

	file := out()
	if err := call(*link, 0, "QueryInterface", uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(file))); err != nil || *file == nil {
		return errShortcutInterface
	}
	defer release(*file)
	return call(*file, 6, "Save", str(path), 1) // IPersistFile::Save
}

// setProcessAppID gives this process the app's AppUserModelID, the shortcut's, before it makes a
// window, so Windows counts the window as the shortcut's.
func setProcessAppID() {
	id, _ := windows.UTF16PtrFromString(brand.AppID)
	procSetProcessAppID.Call(uintptr(unsafe.Pointer(id)))
}

// allowForeground lets process pid take the foreground, which Windows gives only to a process the
// person has just used: the `mur app` they started, asking the open window to come forward.
func allowForeground(pid int) {
	procAllowForeground.Call(uintptr(pid))
}

// FromBundle says whether this process is the app started from its macOS bundle: never here.
func FromBundle() bool { return false }
