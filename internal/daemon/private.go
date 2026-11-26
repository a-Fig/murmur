package daemon

import (
	"fmt"
	"slices"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/tags"
)

// privateRecipientsLocked snapshots the devices that can read a conversation at send time.
// An encrypted but unreadable presence can hide group membership, so group sends wait for
// those presences rather than silently dropping members.
func (d *Daemon) privateRecipientsLocked(addr string) (map[string]string, error) {
	recipients := map[string]string{d.device: bus.BoxPublic(d.boxKey)}
	if addr != "*" && !bus.IsGroup(addr) {
		dev, _ := bus.SplitAddr(addr)
		if dev == d.device {
			return recipients, nil
		}
		key, err := d.privateBoxKeyLocked(dev)
		if err != nil {
			return nil, err
		}
		recipients[dev] = key
		return recipients, nil
	}
	for _, dev := range sortedKeys(d.peers) {
		p := d.peers[dev]
		if d.staleLocked(p) {
			continue
		}
		if addr != "*" {
			if p.locked {
				return nil, noKeyError{fmt.Errorf("can't read group membership from %s yet", dev)}
			}
			if !slices.ContainsFunc(p.pres.Sessions, func(s bus.SessionInfo) bool { return tags.Has(s.Tags, addr) }) {
				continue
			}
		}
		key, err := d.privateBoxKeyLocked(dev)
		if err != nil {
			return nil, err
		}
		recipients[dev] = key
	}
	return recipients, nil
}

func (d *Daemon) privateBoxKeyLocked(dev string) (string, error) {
	if pin := d.st.Pins[dev]; pin != nil && pin.Box != "" {
		return pin.Box, nil
	}
	return "", noKeyError{fmt.Errorf("no verified encryption key for %s", dev)}
}
