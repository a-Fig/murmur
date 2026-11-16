//go:build linux && desktop

package desktop

import (
	"os"
	"path/filepath"

	"github.com/a-Fig/murmur/internal/brand"
)

// On Linux the launcher is a .desktop file in the user's applications, with the app's icon in the
// user's hicolor theme, which every desktop's menu reads (the freedesktop.org specifications).

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

func launcherPath() string {
	d := dataHome()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "applications", brand.AppID+".desktop")
}

func installLauncher(exe, dir, where string) error {
	icon := filepath.Join(dataHome(), "icons", "hicolor", "256x256", "apps", brand.AppID+".png")
	if err := os.MkdirAll(filepath.Dir(icon), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(icon, iconPNG, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return err
	}
	return os.WriteFile(where, []byte(desktopEntry(exe)), 0o644)
}

// FromBundle says whether this process is the app started from its macOS bundle: never here.
func FromBundle() bool { return false }

func setProcessAppID() {}

func allowForeground(int) {}
