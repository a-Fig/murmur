package web

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proc"
)

// ThemePath is the shared theme file for this user's desktop app and daemons.
func ThemePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, brand.Name, "app", "theme"), nil
}

// ReadTheme also serves the app before it creates its native window. An empty or unknown theme
// takes the default, and leaves room for a choice imported from old page storage.
func ReadTheme(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	theme, err := readThemeFile(path)
	if !validTheme(theme) {
		theme = ""
	}
	return theme, err
}

func readThemeFile(path string) (string, error) {
	var b []byte
	err := retryThemeFile(func() (err error) {
		b, err = os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
	return strings.TrimSpace(string(b)), err
}

func saveTheme(path, theme string, importing bool) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Reading and filling an empty preference is one operation across all daemons.
	var lock *proc.Held
	if err := retryThemeFile(func() (err error) {
		lock, err = proc.Lock(path + ".lock")
		return err
	}); err != nil {
		return "", err
	}
	defer lock.Close()
	current, err := readThemeFile(path)
	if err != nil || current == theme || importing && current != "" {
		if !validTheme(current) {
			current = ""
		}
		return current, err
	}
	f, err := os.CreateTemp(dir, ".theme-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(theme + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = retryThemeFile(func() error { return os.Rename(f.Name(), path) })
	}
	return theme, err
}

// Give another daemon time to release the lock and, on Windows, the app time to finish
// reading the file so a replacement can go through.
func retryThemeFile(op func() error) error {
	for try := 0; ; try++ {
		err := op()
		if err == nil || try == 19 || runtime.GOOS != "windows" && !errors.Is(err, proc.ErrLocked) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
