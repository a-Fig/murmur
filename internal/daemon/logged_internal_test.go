package daemon

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// A write that fails is logged by the event's type, ID and addresses. A message's subject stays
// out of the log, which is sent to whoever helps debug a device.
func TestAFailedWriteLogsNoSubject(t *testing.T) {
	var out bytes.Buffer
	d := &Daemon{dir: t.TempDir(), log: log.New(&out, "", 0), device: "here-0001", bgCtx: context.Background(),
		repo: &bus.Repo{Git: gitx.Git{Dir: filepath.Join(t.TempDir(), "no-such-git-dir")}}}
	// With a team key, so the write gets as far as git, and fails there.
	d.boxKey, d.teamKeys = bus.NewBoxKey(), bus.Keyring{}
	withEpoch(d, true)
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "here-0001/aaaaaa",
		To: "peer-0001/bbbbbb", Subject: "the launch moves to Friday"}
	d.appendSoon(e)
	d.bg.Wait()
	got := out.String()
	if !strings.Contains(got, "writing message "+e.ID+" from here-0001/aaaaaa to peer-0001/bbbbbb: ") {
		t.Fatalf("the failed write wasn't logged by type, ID and addresses:\n%s", got)
	}
	if strings.Contains(got, "Friday") {
		t.Fatalf("the failed write's log line holds the subject:\n%s", got)
	}
}
