package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/update"
)

// runUpdates sets how Murmur updates itself on this machine (#1045), goes back to stable from a
// prerelease, or says what applies in this clone. It writes the global git config, as the page's
// Settings does; a running daemon takes the change at its next check, within the hour, where the
// page asks its daemon for one at once.
func runUpdates(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("updates")
	fs.Parse(args)
	if fs.NArg() > 1 {
		return fmt.Errorf("updates takes off, on, nightly or back")
	}
	gitDir, err := client.FindGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	var s update.Setting
	switch arg := fs.Arg(0); arg {
	case "":
		s = update.Read(ctx, os.Getenv, gitDir, version)
	case "back":
		if s, err = update.Back(ctx, os.Getenv, gitDir, version); err != nil {
			return err
		}
	default:
		if !update.ValidChoice(arg) {
			return fmt.Errorf("updates takes off, on, nightly or back, not %q", arg)
		}
		if s, err = update.Set(ctx, os.Getenv, gitDir, version, arg); err != nil {
			return err
		}
	}
	fmt.Fprint(out, describeUpdates(s, fs.Arg(0) != ""))
	return nil
}

func describeUpdates(s update.Setting, changed bool) string {
	var b strings.Builder
	b.WriteString(map[string]string{
		update.ChoiceOff:     "Updates are off on this machine: new releases install only when you click Update now in " + brand.Command + " ui's Settings.",
		update.ChoiceOn:      "Updates are on: stable releases install on their own.",
		update.ChoiceNightly: "Updates are on, nightly: prereleases and nightlies install too.",
	}[s.Choice] + "\n")
	for _, line := range []string{s.Why, s.Hold} {
		if line != "" {
			b.WriteString(line + "\n")
		}
	}
	if changed {
		fmt.Fprintf(&b, "A running daemon takes this at its next check, within the hour; %s ui's Settings checks at once.\n", brand.Command)
	}
	return b.String()
}
