package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/daemon"
)

// RefsSet is what RefsOnly did.
type RefsSet struct {
	Remote string
	// Made are the refs it created on the remote; Had, the ones already there, left alone.
	Made, Had []string
}

// RefsOnly sets up the bus without editing the index, worktree or any branch. On a fresh mesh,
// the first leased team push chooses the owner before the names ref makes the repo joinable.
// Existing meshes keep their team history and ownership, if any.
func RefsOnly(ctx context.Context, dir string) (RefsSet, error) {
	var s RefsSet
	top, err := Top(ctx, dir)
	if err != nil {
		return s, err
	}
	g, _, err := gitIn(ctx, top)
	if err != nil {
		return s, err
	}
	if s.Remote, err = bus.RemoteFor(ctx, g); err != nil {
		return s, err
	}
	// Signed as init, not as the user: the bus's commits never carry a person's git identity.
	repo := &bus.Repo{Git: g, Remote: s.Remote, Name: brand.Command + " init", Email: "init@" + brand.Name + ".invalid"}
	there, err := repo.ListRemote(ctx)
	if err != nil {
		return s, err
	}
	names, err := bus.NamesGenesis(time.Now())
	if err != nil {
		return s, err
	}
	refs := []struct{ name, msg string }{}
	if there[brand.TeamRef] != "" {
		s.Had = append(s.Had, brand.TeamRef)
	} else if !hasMesh(there) {
		gitDir, err := client.FindGitDir(ctx, top)
		if err != nil {
			return s, err
		}
		team, err := daemon.InitialTeamRecord(ctx, gitDir)
		if err != nil {
			return s, err
		}
		refs = append(refs, struct{ name, msg string }{brand.TeamRef, team})
	}
	refs = append(refs, struct{ name, msg string }{brand.NamesRef, names})
	for _, ref := range refs {
		if _, ok := there[ref.name]; ok {
			s.Had = append(s.Had, ref.name)
			continue
		}
		sha, err := repo.NewCommit(ctx, "", ref.msg)
		if err != nil {
			return s, err
		}
		switch err := repo.Push(ctx, sha, ref.name, bus.Lease, ""); {
		case errors.Is(err, bus.ErrRejected):
			if tip, checkErr := repo.RemoteRef(ctx, ref.name); checkErr != nil || tip == "" {
				return s, fmt.Errorf("creating %s: %w", ref.name, err)
			}
			s.Had = append(s.Had, ref.name) // made meanwhile, by another init or a daemon
		case err != nil:
			return s, err
		default:
			s.Made = append(s.Made, ref.name)
		}
	}
	return s, nil
}

func hasMesh(refs map[string]string) bool {
	for ref := range refs {
		if ref == brand.NamesRef || strings.HasPrefix(ref, brand.MeshPrefix) || strings.HasPrefix(ref, brand.PresencePrefix) {
			return true
		}
	}
	return false
}
