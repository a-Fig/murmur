package main

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/a-Fig/murmur/internal/web"
)

// devUpdates is the page's update setting (#1045) kept in memory, as update.Read and update.Set
// would report it: override stands for this repo's git config setting murmur.update false.
type devUpdates struct {
	src *fixtures

	mu       sync.Mutex
	choice   string
	override bool
	skip     string
}

func (u *devUpdates) setting() web.UpdateSetting {
	s := web.UpdateSetting{Choice: u.choice, Effective: u.choice}
	if u.override && u.choice != web.UpdatesOff {
		s.Effective, s.Why = web.UpdatesOff, "Off here: this repo's git config sets murmur.update."
	}
	u.src.mu.Lock()
	running := u.src.version.Running
	u.src.mu.Unlock()
	if s.Effective != web.UpdatesOff {
		pre := strings.Contains(running, "-")
		s.CanGoBack = pre && u.skip != running
		switch {
		case u.skip == "":
		case u.skip == running:
			s.Hold = "Going back to the latest stable release."
		case s.Effective == web.UpdatesNightly:
			s.Hold = "On stable until the next nightly after " + u.skip + "."
		default:
			s.Hold = "On stable until the next stable release after " + u.skip + "."
		}
	}
	return s
}

func (u *devUpdates) UpdateSetting(ctx context.Context) (web.UpdateSetting, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.setting(), nil
}

func (u *devUpdates) SetUpdates(ctx context.Context, choice string) (web.UpdateSetting, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.choice = choice
	s := u.setting()
	u.src.mu.Lock()
	u.src.version.Off = s.Effective == web.UpdatesOff
	u.src.version.Auto = !u.src.version.Off && u.src.version.Latest != ""
	u.src.mu.Unlock()
	return s, nil
}

// BackToStable acts out the daemon installing the latest stable release, 0.3.0, in place of the
// prerelease running.
func (u *devUpdates) BackToStable(ctx context.Context) (web.UpdateSetting, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.setting()
	if !s.CanGoBack {
		return s, errors.New("this is a stable release already")
	}
	u.src.mu.Lock()
	u.skip = u.src.version.Running
	u.src.version.Latest, u.src.version.Ready, u.src.version.Auto, u.src.version.Install = "0.3.0", true, true, "curl -fsSL https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh | sh"
	u.src.mu.Unlock()
	return u.setting(), nil
}
