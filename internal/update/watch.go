package update

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
)

// One binary serves every clone on a machine, and each clone's daemon has its own updater. When one
// of them installs a release, or install.ps1 or install.sh puts one in place, the others notice
// within watchEvery and restart onto it, through the same wait for handoffs and open pages as after
// an install of their own (#1177). Before, each waited for its own hourly check to find the file
// changed, and for up to an hour a machine ran two versions from one file.

// watchEvery is how often the executable is looked at for another binary in its place (Options.Watch).
const watchEvery = 5 * time.Second

// replaced says the file at exe is no longer the binary this process started from: another one is
// in its place. A file that can't be read, as while an installer of the old kind has moved the
// binary aside, is no replacement yet.
func replaced(exe string, original os.FileInfo) bool {
	current, err := Identity(exe)
	return err == nil && !os.SameFile(original, current)
}

// binaryVersion is the release version of the binary at path, as `mur version` prints it ("mur
// 0.5.6"); "" if it doesn't run or says no release. Its build info won't do: the release builds
// with -trimpath, which leaves -ldflags, and the version with it, out. It runs in the temp folder,
// so the note it may add about a newer release finds no clone and asks git nothing more.
func binaryVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Dir = os.TempDir()
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return printedVersion(string(out))
}

// printedVersion is the release version in the first line `mur version` prints, "" for none.
func printedVersion(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != brand.Command {
		return ""
	}
	v := strings.TrimPrefix(fields[1], "v")
	if _, _, ok := version(v); !ok {
		return ""
	}
	return v
}
