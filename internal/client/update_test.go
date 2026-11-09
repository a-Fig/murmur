package client

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proc"
)

func TestUpdatedMCPStartsInstalledBinary(t *testing.T) {
	for _, c := range []struct{ input, want string }{
		{"mur.exe.old-.mur-update-123", "mur.exe"},
		{"mur (deleted)", "mur"},
		{"mur.exe", "mur.exe"},
		{filepath.Join("dir.old-.mur-update-name", "mur.exe"), filepath.Join("dir.old-.mur-update-name", "mur.exe")},
	} {
		if got := installedExecutable(c.input); got != c.want {
			t.Errorf("installedExecutable(%q)=%q, want %q", c.input, got, c.want)
		}
	}
}

// started is what the detached child records: its argv, and the address of the page's socket it
// inherited, if it was handed one, or why it couldn't use it.
type started struct {
	Args []string
	Page string
}

// The detached child exits after recording what it was started with. No daemon or network runs.
func TestMain(m *testing.M) {
	if path := os.Getenv("MURMUR_TEST_DAEMON_ARGS"); path != "" && len(os.Args) > 1 && os.Args[1] == "daemon" {
		rec := started{Args: os.Args[1:]}
		var page net.Listener
		if f := proc.Inherited(brand.PageListenerEnv, "page"); f != nil {
			var err error
			if err = proc.Unpoll(f); err == nil {
				page, err = net.FileListener(f)
			}
			f.Close()
			if err != nil {
				rec.Page = "error: " + err.Error()
			} else {
				rec.Page = page.Addr().String()
			}
		}
		b, err := json.Marshal(rec)
		if err == nil {
			err = os.WriteFile(path, b, 0600)
		}
		if err != nil {
			os.Exit(1)
		}
		if page != nil {
			// Serve one request from the inherited socket, as the new daemon serves the tab's.
			served := make(chan struct{})
			var once sync.Once
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "child")
				once.Do(func() { close(served) })
			})}
			go srv.Serve(page)
			select {
			case <-served:
			case <-time.After(20 * time.Second):
			}
			// Shutdown returns once the answer has gone out and the connection is idle.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			srv.Shutdown(ctx)
			cancel()
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartDaemonForwardsFlags(t *testing.T) {
	gitDir := stateOnly(t)
	captured := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("MURMUR_TEST_DAEMON_ARGS", captured)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	customLog := filepath.Join(t.TempDir(), "custom daemon.log")
	flags := []string{"--poll=17s", "--idle=-1s", "--log=" + customLog}
	if err := StartDaemon(exe, gitDir, nil, flags...); err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{"daemon"}, flags...), "--git-dir", gitDir)
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		b, err := os.ReadFile(captured)
		var rec started
		if err != nil || json.Unmarshal(b, &rec) != nil {
			continue
		}
		got := rec.Args
		if rec.Page != "" {
			t.Fatalf("a daemon started without the page's socket was told of one: %q", rec.Page)
		}
		// The Windows WMI fallback additionally passes the inherited log's name.
		if len(got) == len(want)+2 && got[len(want)] == "--log" && got[len(want)+1] == customLog {
			got = got[:len(want)]
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("child argv=%q, want %q", got, want)
		}
		if _, err := os.Stat(customLog); err != nil {
			t.Fatalf("custom log was not used: %v", err)
		}
		return
	}
	t.Fatal("detached child did not record its arguments")
}

// A daemon restarting onto an update hands the page's socket to the new one (#255): the child
// inherits it, is told its descriptor, and serves from it, on each OS. Here the parent serves from
// the socket first, as the old daemon does, which on Windows ties it to the parent's completion
// port; a request sent between the two waits for the child.
func TestStartDaemonHandsOverThePage(t *testing.T) {
	gitDir := stateOnly(t)
	captured := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("MURMUR_TEST_DAEMON_ARGS", captured)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	page, err := ln.(*net.TCPListener).File()
	ln.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	addr := ln.Addr().String()
	serve, err := net.FileListener(page)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "parent") })}
	go srv.Serve(serve)
	if got := fetch(t, addr); got != "parent" {
		t.Fatalf("before the handoff: %q", got)
	}
	srv.Close()

	// A request in the gap, sent before the child starts, and answered by whoever serves next.
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("connecting in the gap: %v", err)
	}
	defer conn.Close()
	req, err := http.NewRequest("GET", "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("sending in the gap: %v", err)
	}

	if err := StartDaemon(exe, gitDir, page); err != nil {
		t.Fatal(err)
	}
	page.Close() // as the restarting daemon does once the new one is started
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		b, err := os.ReadFile(captured)
		var rec started
		if err != nil || json.Unmarshal(b, &rec) != nil {
			continue
		}
		// A start through WMI (Windows, in a job that forbids breakaway) can't hand anything over,
		// and then tells the child of nothing.
		wmi := len(rec.Args) >= 2 && rec.Args[len(rec.Args)-2] == "--log"
		if wmi && rec.Page == "" {
			t.Log("started through WMI, which hands nothing over")
			return
		}
		if rec.Page != addr {
			t.Fatalf("the child's page socket: %q, want %s", rec.Page, addr)
		}
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), req)
		if err != nil {
			t.Fatalf("the request sent in the gap: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "child" {
			t.Fatalf("the request sent in the gap got %q, want the child's answer", body)
		}
		return
	}
	t.Fatal("detached child did not record what it was started with")
}

// fetch GETs the root of addr and returns the body, or the error as text.
func fetch(t *testing.T, addr string) string {
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("http://" + addr + "/")
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
