package daemon_test

import (
	"crypto/rand"
	"sync"
)

// pushGate is a NameSource that blocks while it is shut, so that a test can hold the assigner's
// push in flight: the assigner picks names outside the daemon's lock. held is signalled when a
// pick waits at the gate.
type pushGate struct {
	mu   sync.Mutex
	cond *sync.Cond
	open bool
	held chan struct{}
}

func newPushGate(open bool) *pushGate {
	g := &pushGate{open: open, held: make(chan struct{}, 1)}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *pushGate) Read(p []byte) (int, error) {
	g.mu.Lock()
	for !g.open {
		select {
		case g.held <- struct{}{}:
		default:
		}
		g.cond.Wait()
	}
	g.mu.Unlock()
	return rand.Read(p)
}

func (g *pushGate) set(open bool) {
	g.mu.Lock()
	g.open = open
	g.cond.Broadcast()
	g.mu.Unlock()
}
