package setup

import (
	"context"
	"errors"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// RefsSet is what RefsOnly did.
type RefsSet struct {
	Remote string
	// Made are the refs it created on the remote; Had, the ones already there, left alone.
	Made, Had []string
}

// RefsOnly sets the repo up, as every `mur init` that pushes does, and all `mur init --refs-only`
// does (#830): it creates refs/murmur/names and refs/murmur/team on the clone's remote in their
// empty state, one root commit each on the empty tree (bus.NamesGenesis, bus.TeamGenesis), which is
// what `mur join` looks for. Each commit is written with commit-tree and pushed as
// <sha>:<ref>, leased on the ref not being there: the working tree, the index and every branch stay
// as they were, a ref already on the remote is left alone, and of two inits at once, one makes each
// ref. The per-device refs are each daemon's to make, after its clone joins.
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
	for _, ref := range []struct{ name, msg string }{{brand.NamesRef, names}, {brand.TeamRef, bus.TeamGenesis()}} {
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
			s.Had = append(s.Had, ref.name) // made meanwhile, by another init or a daemon
		case err != nil:
			return s, err
		default:
			s.Made = append(s.Made, ref.name)
		}
	}
	return s, nil
}
