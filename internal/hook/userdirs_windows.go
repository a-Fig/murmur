package hook

import (
	"os"

	"golang.org/x/sys/windows"
)

// fillUserDirs sets, where they are unset, the variables Go finds the user's folders by on Windows
// (os.UserConfigDir, os.UserCacheDir, os.UserHomeDir), from Windows' own record of the folders.
// One that is set is left as it is: a user who moved APPDATA meant it.
func fillUserDirs() {
	for v, id := range map[string]*windows.KNOWNFOLDERID{
		"APPDATA":      windows.FOLDERID_RoamingAppData,
		"LOCALAPPDATA": windows.FOLDERID_LocalAppData,
		"USERPROFILE":  windows.FOLDERID_Profile,
	} {
		if os.Getenv(v) != "" {
			continue
		}
		if dir, err := windows.KnownFolderPath(id, 0); err == nil && dir != "" {
			os.Setenv(v, dir)
		}
	}
}
