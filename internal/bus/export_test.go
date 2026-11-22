package bus

import "context"

// SetTestBeforeSet sets testBeforeSet for the package's external tests.
func SetTestBeforeSet(f func(refs []string)) { testBeforeSet = f }

// HoldSet takes the lock setRefs takes, as a Fetch setting refs would hold it, and returns its
// release.
func (r *Repo) HoldSet() func() {
	if err := r.setMu.lock(context.Background()); err != nil {
		panic(err)
	}
	return r.setMu.unlock
}
