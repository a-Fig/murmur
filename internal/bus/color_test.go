package bus

import (
	"testing"
	"time"
)

// Tests of devices' colours (#573): one rule gives each device its colour, the same on every page.

var colorT0 = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

// A colour two devices picked goes to the one that picked it first, whatever order they are read
// in; the other is given one as though it had picked none.
func TestTheFirstToPickAColourKeepsIt(t *testing.T) {
	a := ColorPick{Device: "ada-mbp-1111", Color: 3, At: colorT0.Add(time.Minute)}
	b := ColorPick{Device: "bob-mbp-2222", Color: 3, At: colorT0}
	for _, devs := range [][]ColorPick{{a, b}, {b, a}} {
		got := DeviceColors(devs)
		if got[b.Device] != (DeviceColor{Color: 2, Picked: true}) {
			t.Errorf("the first to pick it: %+v", got[b.Device])
		}
		if c := got[a.Device]; c.Picked || c.Color == 2 {
			t.Errorf("the second: %+v", c)
		}
	}
	// At the same moment, the lower ID keeps it.
	a.At = b.At
	if got := DeviceColors([]ColorPick{b, a}); !got[a.Device].Picked || got[b.Device].Picked {
		t.Errorf("a tie: %+v", got)
	}
}

// A device that picked none gets the colour its ID hashes to, unless someone picked that one: then
// the next nobody picked.
func TestAutomaticColoursStepAroundPickedOnes(t *testing.T) {
	auto := ColorPick{Device: "cat-xps-3333"}
	own := autoColor(auto.Device)
	if got := DeviceColors([]ColorPick{auto})[auto.Device]; got != (DeviceColor{Color: own}) {
		t.Fatalf("alone: %+v, want colour %d", got, own)
	}
	picks := []ColorPick{auto,
		{Device: "dan-1", Color: own + 1, At: colorT0},
		{Device: "eve-2", Color: (own+1)%Colors + 1, At: colorT0}}
	if got := DeviceColors(picks)[auto.Device]; got.Color != (own+2)%Colors || got.Picked {
		t.Errorf("with its colour and the next picked: %+v, want %d", got, (own+2)%Colors)
	}
	// With every colour picked, it keeps its own.
	picks = []ColorPick{auto}
	for c := 1; c <= Colors; c++ {
		picks = append(picks, ColorPick{Device: "dev-" + string(rune('a'+c)), Color: c, At: colorT0})
	}
	if got := DeviceColors(picks)[auto.Device]; got.Color != own {
		t.Errorf("with every colour picked: %+v", got)
	}
}

// The page hashed a device's ID to its colour before colours could be picked (#560); the hash is
// the same, so a device that picks none keeps the colour it had.
func TestAutoColourIsThePagesHash(t *testing.T) {
	// From the page's deviceColor before #573, run in Node.
	for id, want := range map[string]int{"tyler-mbp-7a20": 6, "maya-mbp-91c0": 9, "sam-desktop-07be": 5} {
		if got := autoColor(id); got != want {
			t.Errorf("%s: %d, the page had %d", id, got, want)
		}
	}
}

// Clean drops a colour that isn't in the palette, or has no time to settle a race by.
func TestPresenceCleanDropsABadColour(t *testing.T) {
	for _, p := range []Presence{
		{Device: "d-1", Color: Colors + 1, ColorAt: colorT0},
		{Device: "d-1", Color: -1, ColorAt: colorT0},
		{Device: "d-1", Color: 2},
	} {
		p.Clean()
		if p.Color != 0 || !p.ColorAt.IsZero() {
			t.Errorf("kept %d at %v", p.Color, p.ColorAt)
		}
	}
	p := Presence{Device: "d-1", Color: Colors, ColorAt: colorT0}
	if p.Clean(); p.Color != Colors {
		t.Errorf("dropped a good colour")
	}
}
