package daemon

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/tags"
)

func TestRepoModeScopesPrivateMessagesToTheirDevices(t *testing.T) {
	d, p := keyRig(t)
	d.st.TeamOwner = &meshOwner{Policy: bus.RepoPolicy{EncryptionMode: bus.EncryptionPrivate, AllowFiles: true}}
	memberKey, outsiderKey := bus.NewBoxKey(), bus.NewBoxKey()
	d.st.Pins["peer-0001"].Box = bus.BoxPublic(memberKey)
	d.st.Pins["peer-0002"] = &keyPin{Box: bus.BoxPublic(outsiderKey)}
	d.peers["peer-0002"] = &peer{}
	p.pres.Sessions = []bus.SessionInfo{{ID: "bbbbbb", Tags: []tags.Tag{{Name: "group:demo"}}}}

	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "me-0001/aaaaaa", To: "peer-0001/bbbbbb",
		Subject: "private subject", Body: "private body", At: time.Now().UTC()}
	for _, to := range []string{"peer-0001/bbbbbb", "group:demo", "*"} {
		e.To = to
		box, err := d.encryptEventLocked(e)
		if err != nil {
			t.Fatalf("%s: %v", to, err)
		}
		want := []string{d.device, "peer-0001"}
		if to == "*" {
			want = append(want, "peer-0002")
		}
		if got := box.Box.Recipients(); !slices.Equal(got, want) {
			t.Fatalf("%s: wrapped for %v, want %v", to, got, want)
		}
		member, err := bus.DecryptEvent(box, d.device, bus.Keyring{"peer-0001": memberKey})
		if err != nil || member.Body != e.Body {
			t.Fatalf("%s: recipient couldn't read the draft: %v", to, err)
		}
		_, err = bus.DecryptEvent(box, d.device, bus.Keyring{"peer-0002": outsiderKey})
		if to != "*" && !errors.Is(err, bus.ErrNotForUs) || to == "*" && err != nil {
			t.Fatalf("%s: unexpected outsider result: %v", to, err)
		}
	}
	e.To = "unknown-0001/bbbbbb"
	if _, err := d.encryptEventLocked(e); !errors.As(err, new(noKeyError)) {
		t.Fatalf("private send to a device with no key: %v", err)
	}
	d.st.TeamOwner.Policy.EncryptionMode = bus.EncryptionOff
	clear, err := d.encryptEventLocked(e)
	if err != nil || clear.Box != nil || clear.Body != e.Body {
		t.Fatalf("Off didn't preserve the plaintext: %+v, %v", clear, err)
	}
}
