package desktop

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePNG is a PNG's signature and IHDR, enough for pngSize.
func fakePNG(w, h uint32) []byte {
	b := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	b = binary.BigEndian.AppendUint32(b, w)
	return binary.BigEndian.AppendUint32(b, h)
}

func TestIcoHoldsEachPNG(t *testing.T) {
	big, small := fakePNG(256, 256), fakePNG(32, 32)
	ico := icoFrom(big, small)
	if got := binary.LittleEndian.Uint16(ico[4:]); got != 2 {
		t.Fatalf("%d images, want 2", got)
	}
	// The first entry: 256 px is written as 0, then its size and where it starts.
	if ico[6] != 0 || ico[7] != 0 || binary.LittleEndian.Uint32(ico[14:]) != uint32(len(big)) || binary.LittleEndian.Uint32(ico[18:]) != 6+32 {
		t.Fatalf("first entry: % x", ico[6:22])
	}
	if ico[22] != 32 || binary.LittleEndian.Uint32(ico[34:]) != uint32(6+32+len(big)) {
		t.Fatalf("second entry: % x", ico[22:38])
	}
	if !bytes.HasSuffix(ico, append(append([]byte{}, big...), small...)) {
		t.Fatal("the PNGs aren't at the end, in order")
	}
}

func TestIcnsHoldsThePNG(t *testing.T) {
	p := fakePNG(512, 512)
	icns := icnsFrom(p)
	if string(icns[:4]) != "icns" || binary.BigEndian.Uint32(icns[4:]) != uint32(len(icns)) || string(icns[8:12]) != "ic09" || !bytes.Equal(icns[16:], p) {
		t.Fatalf("icns: % x", icns[:16])
	}
}

func TestInfoPlistNamesTheBundle(t *testing.T) {
	plist := string(infoPlist())
	for _, want := range []string{"<key>CFBundleExecutable</key>\n\t<string>Murmur</string>", "<key>CFBundleIdentifier</key>\n\t<string>dev.afig.murmur</string>", "<string>APPL</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("Info.plist lacks %q:\n%s", want, plist)
		}
	}
}

// A path is quoted for a .desktop file's Exec key as the freedesktop.org specification has it: in
// double quotes, ", `, $ and \ escaped, and every backslash doubled again by the string escaping.
func TestExecQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/home/ada/.local/bin/mur": `"/home/ada/.local/bin/mur"`,
		"/opt/my apps/mur":         `"/opt/my apps/mur"`,
		`/a"b$c` + "`d":            `"/a\\"b\\$c\\` + "`" + `d"`,
		`/a\b`:                     `"/a\\\\b"`,
		"/opt/100%/mur":            `"/opt/100%%/mur"`,
	} {
		if got := execQuote(in); got != want {
			t.Errorf("execQuote(%q) = %s, want %s", in, got, want)
		}
	}
	if e := desktopEntry("/usr/bin/mur"); !strings.Contains(e, "\nExec=\"/usr/bin/mur\" app --from-launcher\n") || !strings.Contains(e, "\nIcon=dev.afig.murmur\n") {
		t.Errorf("entry:\n%s", e)
	}
}

// The launcher is put in place once, left as it is while it is right, made again when mur moves,
// and taken out. Each system's home for it is pointed at a temporary directory first.
// testHome points each system's home for the launcher at a temporary directory, and lets a
// launcher point at a build in one, which is what the test has.
func testHome(t *testing.T) {
	t.Helper()
	if !Supported {
		t.Skip("this build has no app")
	}
	was := devBuild
	devBuild = func(string) bool { return false }
	t.Cleanup(func() { devBuild = was })
	home := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	if want := launcherPath(); !strings.HasPrefix(want, home) {
		t.Fatalf("the launcher would go to %s, outside the test's home", want)
	}
}

func TestInstallLauncher(t *testing.T) {
	testHome(t)
	dir := t.TempDir()
	exe := filepath.Join(t.TempDir(), "mur.exe")
	os.WriteFile(exe, nil, 0o755)

	// Something of the person's beside where the launcher goes is left as it is (Sol review).
	theirs := launcherPath() + ".new"
	os.MkdirAll(filepath.Dir(theirs), 0o755)
	os.WriteFile(theirs, []byte("theirs"), 0o644)
	where, changed, err := InstallLauncher(exe, dir)
	if err != nil || !changed || !exists(where) {
		t.Fatalf("first install: %s, changed %v, %v", where, changed, err)
	}
	if b, _ := os.ReadFile(theirs); string(b) != "theirs" {
		t.Fatal("removed what was beside the launcher")
	}
	os.Remove(theirs)
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(where), ".*")); len(left) > 0 {
		t.Fatalf("left behind %q", left)
	}
	if _, changed, err := InstallLauncher(exe, dir); err != nil || changed {
		t.Fatalf("second install changed it (%v) or failed: %v", changed, err)
	}
	moved := filepath.Join(t.TempDir(), "mur.exe")
	os.WriteFile(moved, nil, 0o755)
	if _, changed, err := InstallLauncher(moved, dir); err != nil || !changed {
		t.Fatalf("install after mur moved: changed %v, %v", changed, err)
	}
	if runtime.GOOS == "darwin" {
		if link, err := os.Readlink(filepath.Join(where, "Contents", "MacOS", bundleExe)); err != nil || link != moved {
			t.Fatalf("the bundle's executable points at %q (%v), want %s", link, err, moved)
		}
	}
	if got, err := RemoveLauncher(dir); err != nil || got != where || exists(where) {
		t.Fatalf("remove: %s, %v; still there: %v", got, err, exists(where))
	}
}

// A launcher the person took out stays out, until mur moves; one of the same name that isn't
// Murmur's is neither replaced nor taken out; a build in a temporary directory gets none.
func TestLauncherLeavesWhatIsntOurs(t *testing.T) {
	testHome(t)
	dir := t.TempDir()
	exe := filepath.Join(t.TempDir(), "mur.exe")
	os.WriteFile(exe, nil, 0o755)

	where, _, err := InstallLauncher(exe, dir)
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(where)
	if got, changed, err := InstallLauncher(exe, dir); got != "" || changed || err != nil || exists(where) {
		t.Fatalf("a launcher the person took out came back: %s, %v, %v", got, changed, err)
	}

	os.Remove(filepath.Join(dir, stampFile))
	os.WriteFile(where, []byte("someone else's"), 0o644)
	if _, _, err := InstallLauncher(exe, dir); err == nil {
		t.Fatal("put Murmur's launcher over someone else's")
	}
	if got, err := RemoveLauncher(dir); got != "" || err != nil {
		t.Fatalf("took out someone else's launcher: %s, %v", got, err)
	}
	if b, _ := os.ReadFile(where); string(b) != "someone else's" {
		t.Fatal("someone else's launcher changed")
	}

	os.Remove(where)
	devBuild = func(string) bool { return true }
	if got, _, _ := InstallLauncher(exe, dir); got != "" || exists(where) {
		t.Fatal("a temporary build got a launcher")
	}
}
