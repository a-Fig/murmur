package bus_test

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// A group's tag is an address (#184), and group.add carries an agent's add of a session to groups.
func TestGroupAddresses(t *testing.T) {
	for to, want := range map[string]bool{
		"group:ui":       true,
		"group:":         false,
		"group:ui/web":   false,
		"reviewing:x":    false,
		"model:opus-5.5": false,
		"Group:ui":       false,
	} {
		if got := bus.ValidTo(to); got != want {
			t.Errorf("ValidTo(%q) = %v; want %v", to, got, want)
		}
	}
	msg := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "dev-0001/aaaaaa", At: time.Now(),
		To: "group:ui", Subject: "hello"}
	if !msg.Valid() {
		t.Error("a message to a group isn't valid")
	}
}

func TestGroupAddIsHeldToGroups(t *testing.T) {
	add := func(to string, add, remove []string) bus.Event {
		return bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.GroupAdd, From: "dev-0001/aaaaaa", At: time.Now(),
			To: to, Add: add, Remove: remove}
	}
	if e := add("dev-0002/bbbbbb", []string{"group:ui", "group:bus"}, nil); !e.Valid() {
		t.Error("an add to two groups isn't valid")
	}
	for what, e := range map[string]bus.Event{
		"do-not-disturb":      add("dev-0002/bbbbbb", []string{"do-not-disturb"}, nil),
		"a group and a label": add("dev-0002/bbbbbb", []string{"group:ui", "reviewing"}, nil),
		"a removal":           add("dev-0002/bbbbbb", []string{"group:ui"}, []string{"group:old"}),
		"only a removal":      add("dev-0002/bbbbbb", nil, []string{"group:ui"}),
		"no groups":           add("dev-0002/bbbbbb", nil, nil),
		"a device":            add("dev-0002", []string{"group:ui"}, nil),
		"a person":            add("dev-0002/"+bus.PersonSession, []string{"group:ui"}, nil),
		"everyone":            add("*", []string{"group:ui"}, nil),
	} {
		if e.Valid() {
			t.Errorf("an add of %s is valid: %+v", what, e)
		}
	}
}
