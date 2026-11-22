package daemon

import (
	"io"
	"log"
	"slices"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// A name is taken for a device's nickname, and a send to it refused (#542), only when it is no
// address: not a person's name, and not the ID of a device, whether its presence is here or only
// its log.
func TestANicknameIsNamedOnlyWhenItIsNoAddress(t *testing.T) {
	t.Parallel()
	d := &Daemon{device: "here-0001", log: log.New(io.Discard, "", 0), peers: map[string]*peer{}}
	d.st.Nick = "tyler"
	d.peers["laptop-0002"] = &peer{pres: bus.Presence{Nick: "tyler"}}
	d.peers["desk-0003"] = &peer{pres: bus.Presence{Nick: "desk-0004"}}
	d.peers["home-0005"] = &peer{pres: bus.Presence{Nick: "home"}}
	d.peers["ada-0006"] = &peer{pres: bus.Presence{Sessions: []bus.SessionInfo{{ID: bus.PersonSession, Name: "ada"}}}}
	d.peers["other-0007"] = &peer{pres: bus.Presence{Nick: "ada"}}
	d.peers["desk-0004"] = &peer{}

	for _, c := range []struct {
		name string
		want []string
	}{
		{"tyler", []string{"here-0001", "laptop-0002"}}, // this device's own, and another's
		{"home", []string{"home-0005"}},
		{"ada", nil},       // a person's name, which is an address
		{"desk-0004", nil}, // a device's ID, which stands first
		{"nobody", nil},
	} {
		if got := d.calledLocked(c.name); !slices.Equal(got, c.want) {
			t.Errorf("%s names %v, want %v", c.name, got, c.want)
		}
	}

	// A device whose presence has gone is still an address while its log is on the bus: a message
	// to it waits there. A nickname of that ID doesn't stand in its way.
	delete(d.peers, "desk-0004")
	if got := d.calledLocked("desk-0004"); got == nil {
		t.Fatalf("with no log, desk-0004 names nothing; the test needs it to name desk-0003")
	}
	d.st.Processed = map[string]string{brand.MeshPrefix + "desk-0004": "abc"}
	if got := d.calledLocked("desk-0004"); got != nil {
		t.Errorf("the ID of a device whose log is processed names %v", got)
	}
	d.st.Processed = nil
	d.known = map[string]string{brand.MeshPrefix + "desk-0004": "abc"}
	if got := d.calledLocked("desk-0004"); got != nil {
		t.Errorf("the ID of a device whose log is fetched names %v", got)
	}
}
