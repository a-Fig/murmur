package daemon

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// Two devices can share a group's tag: laptop-1a2b, an ID from before #330, and device-laptop-1a2b
// both have group:device-laptop-1a2b. A send to it goes to neither, rather than to whichever is
// looked at first, and the sender is told to use the ID.
func TestASendToADeviceGroupTwoDevicesShareIsRefused(t *testing.T) {
	t.Parallel()
	for _, self := range []string{"here-0001", "laptop-1a2b"} {
		d := &Daemon{device: self, log: log.New(io.Discard, "", 0), peers: map[string]*peer{}}
		d.peers["device-laptop-1a2b"] = &peer{}
		if self != "laptop-1a2b" {
			d.peers["laptop-1a2b"] = &peer{}
		}
		r := d.send(context.Background(), "aaaaaa", proto.Request{To: "group:device-laptop-1a2b", Subject: "which one?"})
		if !strings.Contains(r.Error, "device-laptop-1a2b and laptop-1a2b both have the group group:device-laptop-1a2b") {
			t.Errorf("on %s, a send to the shared group got %q, want it refused", self, r.Error)
		}
	}
}
