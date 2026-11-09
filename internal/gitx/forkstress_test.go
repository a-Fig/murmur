package gitx_test

// DEBUG for #217, not to be merged: many goroutines start `git` at once, as the daemons do, while
// others keep the runtime busy, and every Start that takes over 20 s is counted as a hang. Runs
// only with FORKSTRESS set to a duration.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestForkStress(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("FORKSTRESS"))
	if err != nil {
		t.Skip("FORKSTRESS not set")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("git is %s", git)
	end := time.Now().Add(d)
	var starts, hangs, slow atomic.Int64
	var maxStart atomic.Int64
	stop := make(chan struct{})
	// Busy work: allocation, and loopback connections opened and closed (fds come and go).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { buf := make([]byte, 64); c.Read(buf); c.Close() }()
		}
	}()
	var bg sync.WaitGroup
	for i := 0; i < 4; i++ {
		bg.Add(1)
		go func() {
			defer bg.Done()
			var keep [][]byte
			for {
				select {
				case <-stop:
					return
				default:
				}
				keep = append(keep, make([]byte, 64<<10))
				if len(keep) > 200 {
					keep = nil
				}
				if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
					c.Write([]byte("hi"))
					c.Close()
				}
			}
		}()
	}
	var dumped atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(end) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				cmd := exec.CommandContext(ctx, git, "--version")
				started := make(chan error, 1)
				t0 := time.Now()
				go func() { started <- cmd.Start() }()
				select {
				case err := <-started:
					el := time.Since(t0)
					if el > 2*time.Second {
						slow.Add(1)
					}
					for m := maxStart.Load(); int64(el) > m && !maxStart.CompareAndSwap(m, int64(el)); m = maxStart.Load() {
					}
					if err == nil {
						cmd.Wait()
					}
					starts.Add(1)
				case <-time.After(20 * time.Second):
					hangs.Add(1)
					fmt.Fprintf(os.Stderr, "FORKSTRESS hang: Start of git has not returned after 20 s (%s)\n", time.Now().Format("15:04:05"))
					if dumped.CompareAndSwap(false, true) {
						pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
					}
					// This goroutine leaves the stuck Start behind and goes on.
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	close(stop)
	bg.Wait()
	msg := fmt.Sprintf("FORKSTRESS result: %d starts, %d over 2 s, %d hangs over 20 s, slowest %v", starts.Load(), slow.Load(), hangs.Load(), time.Duration(maxStart.Load()))
	fmt.Fprintln(os.Stderr, msg)
	if hangs.Load() > 0 {
		t.Error(msg)
	}
}
