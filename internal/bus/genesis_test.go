package bus_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// The team ref counts as empty only at the commit init writes: a root on the empty tree with the
// genesis message. The same message on top of other commits, or on a tree that holds files, is
// not, so a daemon never takes a team ref with history it couldn't read for one with none (#830).
func TestTeamGenesisIsARootOnTheEmptyTree(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ctx := context.Background()
	r := &bus.Repo{Git: gitx.Git{Dir: dir}, Name: "test", Email: "test@example.invalid"}
	genesis, err := r.NewCommit(ctx, "", bus.TeamGenesis())
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.NewCommit(ctx, "", "an earlier record")
	if err != nil {
		t.Fatal(err)
	}
	onTop, err := r.NewCommit(ctx, other, bus.TeamGenesis())
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := r.Git.Run(ctx, "x\n", "hash-object", "-w", "--stdin")
	tree, err := r.Git.Run(ctx, "100644 blob "+strings.TrimSpace(blob)+"\tf\n", "mktree")
	if err != nil {
		t.Fatal(err)
	}
	withFiles, err := r.Git.Run(ctx, bus.TeamGenesis(), "-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"commit-tree", strings.TrimSpace(tree), "-F", "-")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		sha  string
		want bool
	}{
		"the genesis":                   {genesis, true},
		"its message with a parent":     {onTop, false},
		"its message on a tree":         {strings.TrimSpace(withFiles), false},
		"another root on an empty tree": {other, false},
	} {
		if got, err := r.IsTeamGenesis(ctx, tc.sha); err != nil || got != tc.want {
			t.Errorf("%s: IsTeamGenesis = %v, %v; want %v", name, got, err, tc.want)
		}
	}
}
