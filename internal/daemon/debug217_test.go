package daemon_test

// DEBUG for #217, not to be merged: on a failure, print every goroutine's stack (the daemons run in
// this process) and each device's daemon log, so a stall shows where it is stuck.

import (
	"fmt"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

var dumpOnce sync.Map // *testing.T -> struct{}

func debugDump(t *testing.T, why string) {
	if _, done := dumpOnce.LoadOrStore(t, struct{}{}); done {
		return
	}
	fmt.Fprintf(os.Stderr, "\n=== DEBUG217 %s %s at %s: goroutines\n", t.Name(), why, time.Now().Format("15:04:05.000000"))
	pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
	fmt.Fprintf(os.Stderr, "=== DEBUG217 end of goroutines\n")
}

// debugLog gives a device a log kept in memory, printed if its test fails.
func debugLog(d *device, tag string) *syncBuf {
	buf := &syncBuf{}
	d.t.Cleanup(func() {
		if d.t.Failed() {
			fmt.Fprintf(os.Stderr, "\n=== DEBUG217 %s log of device %s (%s)\n%s=== DEBUG217 end of log %s\n", d.t.Name(), tag, d.gitDir, buf.String(), tag)
		}
	})
	return buf
}
