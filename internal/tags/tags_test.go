package tags

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestModelTag(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":            "model:opus-5.5",
		"claude-sonnet-4-5-20250929": "model:sonnet-4.5",
		"claude-haiku-5-5":           "model:haiku-5.5",
		"gpt-5.1-codex":              "model:gpt-5.1-codex",
		"GPT-5":                      "model:gpt-5",
		"  ":                         "",
		"some model/with spaces":     "model:some-model-with-spaces",
	} {
		if got := ModelTag(id); got != want {
			t.Errorf("ModelTag(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"reviewing", "model:opus-5.5", "watch:branch/main", "unavailable", "available", "a"} {
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "Reviewing", "-x", ":x", "a b", "model:", "ünicode",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
}

func TestSetSelf(t *testing.T) {
	var st State
	if err := st.SetSelf([]string{"Reviewing", "model:opus-5.5"}, nil); err != nil {
		t.Fatal(err)
	}
	// A second model replaces the first.
	if err := st.SetSelf([]string{"model:sonnet-5.5", "ui"}, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"model:sonnet-5.5", "reviewing", "ui"}; !slices.Equal(st.Self, want) {
		t.Fatalf("Self = %v, want %v", st.Self, want)
	}
	if err := st.SetSelf(nil, []string{"reviewing", "ui"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"model:sonnet-5.5"}; !slices.Equal(st.Self, want) {
		t.Fatalf("Self = %v, want %v", st.Self, want)
	}
	if err := st.SetSelf([]string{"no spaces allowed"}, nil); err == nil {
		t.Fatal("a tag with spaces was taken")
	}
	var many []string
	for _, c := range "abcdefghijklm" {
		many = append(many, string(c))
	}
	if err := st.SetSelf(many, nil); err == nil {
		t.Fatalf("%d tags were taken", len(many)+1)
	}
	if want := []string{"model:sonnet-5.5"}; !slices.Equal(st.Self, want) {
		t.Fatalf("a refused change changed Self to %v", st.Self)
	}
}

func TestEffectivePrecedence(t *testing.T) {
	at := time.Now()
	var st State
	st.SetSelf([]string{"reviewing"}, nil)
	derived := []string{"model:opus-5.5"}

	got := Effective(st.Self, derived, st.Human)
	want := []Tag{{"model:opus-5.5", SrcDerived}, {"reviewing", SrcSession}}
	if !slices.Equal(got, want) {
		t.Fatalf("Effective = %v, want %v", got, want)
	}

	// The session's own model wins over the derived one.
	st.SetSelf([]string{"model:sonnet-5.5"}, nil)
	got = Effective(st.Self, derived, st.Human)
	if !Has(got, "model:sonnet-5.5") || Has(got, "model:opus-5.5") {
		t.Fatalf("Effective = %v: the session's model should replace the derived one", got)
	}

	// A human beats both: release on, reviewing off, and the model replaced.
	base := Base(st.Self, derived)
	if err := st.SetHuman(base, []string{"release", "model:haiku-5.5"}, []string{"reviewing"}, "tyler", at); err != nil {
		t.Fatal(err)
	}
	got = Effective(st.Self, derived, st.Human)
	want = []Tag{{"model:haiku-5.5", SrcHuman}, {"release", SrcHuman}}
	if !slices.Equal(got, want) {
		t.Fatalf("Effective = %v, want %v", got, want)
	}

	// The session adding reviewing back doesn't undo the human's mark.
	st.SetSelf([]string{"reviewing"}, nil)
	if Has(Effective(st.Self, derived, st.Human), "reviewing") {
		t.Fatal("the session overrode a human")
	}

	// A human removing a tag only a human gave leaves no mark.
	base = Base(st.Self, derived)
	if err := st.SetHuman(base, nil, []string{"release"}, "tyler", at); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Human["release"]; ok {
		t.Fatalf("Human = %v: release, which only a human set, left a mark", st.Human)
	}
}

func TestFilter(t *testing.T) {
	ts := []Tag{{Name: "model:opus-5.5"}, {Name: "reviewing"}}
	for _, c := range []struct {
		terms []string
		want  bool
	}{
		{nil, true},
		{[]string{"reviewing"}, true},
		{[]string{"reviewing", "model:opus*"}, true},
		{[]string{"reviewing,model:sonnet*"}, false},
		{[]string{"-reviewing"}, false},
		{[]string{"unavailable"}, false},
		{[]string{"-unavailable"}, true},
		{[]string{"model:*", "-ui"}, true},
	} {
		f, err := ParseFilter(c.terms)
		if err != nil {
			t.Fatalf("ParseFilter(%v): %v", c.terms, err)
		}
		if got := f.Match(ts); got != c.want {
			t.Errorf("filter %v matched %v, want %v", c.terms, got, c.want)
		}
	}
	if _, err := ParseFilter([]string{"a b"}); err == nil {
		t.Error("a term with a space was taken")
	}
	f, _ := ParseFilter([]string{"Model:Opus*", "-ui"})
	if got := f.String(); got != "model:opus*, -ui" {
		t.Errorf("String() = %q", got)
	}
}

func TestCleanRead(t *testing.T) {
	got := CleanRead([]Tag{{Name: "ok"}, {Name: "ok"}, {Name: "Bad"}, {Name: "x", Src: "forged"}})
	want := []Tag{{Name: "ok"}, {Name: "x"}}
	if !slices.Equal(got, want) {
		t.Fatalf("CleanRead = %v, want %v", got, want)
	}
}

func TestAHumansModelStaysTheOnlyOne(t *testing.T) {
	var st State
	st.SetHuman(nil, []string{"model:opus-5.5"}, nil, "tyler", time.Now())
	// The session, then T3, name another model after the human chose one.
	st.SetSelf([]string{"model:sonnet-5.5"}, nil)
	got := Effective(st.Self, []string{"model:haiku-5.5"}, st.Human)
	if want := []Tag{{"model:opus-5.5", SrcHuman}}; !slices.Equal(got, want) {
		t.Fatalf("Effective = %v, want only the human's model", got)
	}
}

func TestTheCapKeepsAHumansTags(t *testing.T) {
	var st State
	var twelve []string
	for _, c := range "abcdefghijkl" {
		twelve = append(twelve, string(c))
	}
	if err := st.SetSelf(twelve, nil); err != nil {
		t.Fatal(err)
	}
	st.SetHuman(st.Self, []string{"reviewing", "zz"}, nil, "tyler", time.Now())
	got := Effective(st.Self, []string{"model:opus-5.5"}, st.Human)
	if len(got) != MaxPerSession || !Has(got, "reviewing") || !Has(got, "zz") || Has(got, "model:opus-5.5") {
		t.Fatalf("Effective = %v: the human's tags should survive the cap, before the session's and the derived", got)
	}
}

// A change read late doesn't overwrite a human's newer choice about the same tag (#251), whichever
// order the two were read in. Times come from different machines' clocks, so a change up to a minute
// older still goes by the order it was read in.
func TestALateChangeLeavesANewerChoice(t *testing.T) {
	const opus, haiku, sonnet = "model:opus-5.5", "model:haiku-5.5", "model:sonnet-5.5"
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	later := at.Add(30 * time.Minute)
	type change struct {
		add, remove []string
		at          time.Time
	}
	for _, c := range []struct {
		name    string
		base    []string
		changes []change
		want    []string
	}{
		{name: "a late removal doesn't take a newer add off",
			changes: []change{{add: []string{"release"}, at: later}, {remove: []string{"release"}, at: at}},
			want:    []string{"release"}},
		{name: "a late add doesn't put a newer removal back on",
			base:    []string{"reviewing"},
			changes: []change{{remove: []string{"reviewing"}, at: later}, {add: []string{"reviewing"}, at: at}}},
		{name: "a late add doesn't bring back a tag a human took away, leaving no mark",
			changes: []change{{add: []string{"release"}, at: at.Add(10 * time.Minute)}, {remove: []string{"release"}, at: later},
				{add: []string{"release"}, at: at}}},
		{name: "a removal read before the older add it came after",
			changes: []change{{remove: []string{"release"}, at: later}, {add: []string{"release"}, at: at}}},
		{name: "a late add doesn't replace a newer one of its family",
			changes: []change{{add: []string{haiku}, at: later}, {add: []string{sonnet}, at: at}},
			want:    []string{haiku}},
		{name: "a late add doesn't bring back a model that was replaced, once the replacement is gone",
			changes: []change{{add: []string{haiku}, at: at}, {add: []string{sonnet}, at: at.Add(10 * time.Minute)},
				{remove: []string{sonnet}, at: later}, {add: []string{haiku}, at: at.Add(5 * time.Minute)}}},
		{name: "nor another model from before the last choice about the family",
			changes: []change{{add: []string{sonnet}, at: at.Add(10 * time.Minute)}, {remove: []string{sonnet}, at: later},
				{add: []string{opus}, at: at}}},
		{name: "changes within the allowance don't wear the latest time down",
			changes: []change{{add: []string{"release"}, at: at.Add(100 * time.Second)}, {add: []string{"release"}, at: at.Add(50 * time.Second)},
				{remove: []string{"release"}, at: at}},
			want: []string{"release"}},
		{name: "nor does a removal within the allowance, over a mark",
			changes: []change{{add: []string{"release"}, at: at.Add(100 * time.Second)}, {remove: []string{"release"}, at: at.Add(50 * time.Second)},
				{add: []string{"release"}, at: at}}},
		{name: "an add, then the removal of another of its family, read in that order",
			base: []string{opus},
			changes: []change{{add: []string{haiku}, at: at.Add(-time.Hour)}, {add: []string{sonnet}, at: at},
				{remove: []string{haiku}, at: later}},
			want: []string{sonnet}},
		{name: "the same two read the other way round",
			base: []string{opus},
			changes: []change{{add: []string{haiku}, at: at.Add(-time.Hour)}, {remove: []string{haiku}, at: later},
				{add: []string{sonnet}, at: at}},
			want: []string{sonnet}},
		{name: "a replacement within the allowance doesn't wear the family's latest time down",
			base: []string{opus},
			changes: []change{{add: []string{opus}, at: at.Add(100 * time.Second)}, {add: []string{haiku}, at: at.Add(50 * time.Second)},
				{add: []string{sonnet}, at: at}},
			want: []string{haiku}},
		{name: "a late add leaves a newer removal in its family",
			base:    []string{opus},
			changes: []change{{remove: []string{opus}, at: later}, {add: []string{sonnet}, at: at}},
			want:    []string{sonnet}},
		{name: "one late tag of a change leaves the rest to apply",
			changes: []change{{add: []string{"release"}, at: later}, {remove: []string{"release"}, add: []string{"urgent"}, at: at}},
			want:    []string{"release", "urgent"}},
		{name: "a newer change applies, over a mark",
			changes: []change{{add: []string{"release"}, at: at}, {remove: []string{"release"}, at: later}}},
		{name: "a newer change applies, over a removal that left no mark",
			changes: []change{{remove: []string{"release"}, at: at}, {add: []string{"release"}, at: later}},
			want:    []string{"release"}},
		{name: "within a minute, the order they were read in stands",
			changes: []change{{add: []string{"release"}, at: at.Add(time.Minute)}, {remove: []string{"release"}, at: at}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var st State
			for _, ch := range c.changes {
				if err := st.SetHuman(c.base, ch.add, ch.remove, "tyler", ch.at); err != nil {
					t.Fatal(err)
				}
			}
			got := Names(Effective(c.base, nil, st.Human))
			if !slices.Equal(got, append([]string{}, c.want...)) {
				t.Errorf("tags = %v, want %v", got, c.want)
			}
		})
	}
}

// A removal that left no mark is still kept, or a late add could bring the tag back.
func TestARemovalLeavingNoMarkIsKept(t *testing.T) {
	var st State
	if err := st.SetHuman(nil, nil, []string{"release"}, "tyler", time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Empty() {
		t.Error("the state counts as empty, so it wouldn't be kept")
	}
	if got := Effective(nil, nil, st.Human); len(got) != 0 {
		t.Errorf("tags = %v, want none", got)
	}
}

// human is Murmur's to give, from a person's address (#267): nobody types it on or off, and nothing
// read off the bus or kept from before carries it.
func TestHumanIsMurmursToGive(t *testing.T) {
	if _, err := Clean([]string{"reviewing", " Human "}); err == nil {
		t.Error("Clean took human")
	}
	var st State
	if err := st.SetSelf([]string{"human"}, nil); err == nil || len(st.Self) != 0 {
		t.Errorf("a session set human: %v %v", err, st.Self)
	}
	if err := st.SetHuman(nil, nil, []string{"human"}, "ada", time.Now()); err == nil {
		t.Error("a human removed human")
	}
	if got := CleanRead([]Tag{{Name: Human, Src: SrcDerived}, {Name: "reviewing"}}); Has(got, Human) || !Has(got, "reviewing") {
		t.Errorf("CleanRead: %+v", got)
	}
	kept := Effective([]string{"human", "reviewing"}, nil, map[string]Mark{Human: {On: true}})
	if Has(kept, Human) || !Has(kept, "reviewing") {
		t.Errorf("Effective: %+v", kept)
	}
	if got := Person(); len(got) != 1 || got[0] != (Tag{Name: Human, Src: SrcDerived}) {
		t.Errorf("Person: %+v", got)
	}
	f, _ := ParseFilter([]string{"human"})
	if !f.Match(Person()) || f.Match(nil) {
		t.Error("who human doesn't find people")
	}
}

// A human tag a session set before #267 takes no place: the session can still have twelve it can
// see, and the old one goes with its next change.
func TestAnOldHumanTagTakesNoPlace(t *testing.T) {
	st := State{Self: []string{"human"}}
	for i := range MaxPerSession - 1 {
		st.Self = append(st.Self, "t"+string(rune('a'+i)))
	}
	if err := st.SetSelf([]string{"twelfth"}, nil); err != nil {
		t.Fatalf("the twelfth tag: %v", err)
	}
	if slices.Contains(st.Self, Human) || len(st.Self) != MaxPerSession {
		t.Errorf("Self: %v", st.Self)
	}
	st = State{Human: map[string]Mark{Human: {On: true}}}
	if err := st.SetHuman(nil, []string{"reviewing"}, nil, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Human[Human]; ok {
		t.Errorf("a human's old mark is kept: %v", st.Human)
	}
}

// A group a session set under the prefix before devices held it takes no place either (#705): it
// isn't shown, can't be removed by hand, and goes with the session's next change.
func TestAnOldDeviceGroupTakesNoPlace(t *testing.T) {
	st := State{Self: []string{"group:device-lab"}}
	for i := range MaxPerSession - 1 {
		st.Self = append(st.Self, "t"+string(rune('a'+i)))
	}
	if err := st.SetSelf([]string{"twelfth"}, nil); err != nil {
		t.Fatalf("the twelfth tag: %v", err)
	}
	if slices.Contains(st.Self, "group:device-lab") || len(st.Self) != MaxPerSession {
		t.Errorf("Self: %v", st.Self)
	}
	st = State{Human: map[string]Mark{"group:device-lab": {On: true}}, Cleared: map[string]time.Time{"group:device-lab": time.Now()}}
	if err := st.SetHuman(nil, []string{"reviewing"}, nil, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Human["group:device-lab"]; ok {
		t.Errorf("a person's old mark is kept: %v", st.Human)
	}
	if _, ok := st.Cleared["group:device-lab"]; ok {
		t.Errorf("an old clearing is kept: %v", st.Cleared)
	}
	for _, tag := range Effective([]string{"group:device-lab", "reviewing"}, nil, nil) {
		if strings.HasPrefix(tag.Name, DevicePrefix) {
			t.Errorf("Effective keeps %s", tag.Name)
		}
	}
}

// A change that fails leaves the state it was copied from as it was, old human tag and all.
func TestAFailedChangeLeavesTheKeptStateAlone(t *testing.T) {
	kept := State{Self: []string{"human", "reviewing"}, Human: map[string]Mark{Human: {On: true}}}
	next := kept // a shallow copy, sharing the slice and the map
	if err := next.SetSelf([]string{"Not A Tag!"}, nil); err == nil {
		t.Fatal("an invalid tag was taken")
	}
	if !slices.Equal(kept.Self, []string{"human", "reviewing"}) {
		t.Errorf("the kept Self changed: %q", kept.Self)
	}
	next = kept
	if err := next.SetHuman(nil, []string{"Not A Tag!"}, nil, "ada", time.Now()); err == nil {
		t.Fatal("an invalid tag was taken")
	}
	if _, ok := kept.Human[Human]; !ok {
		t.Errorf("the kept marks changed: %v", kept.Human)
	}
}

func TestIsGroup(t *testing.T) {
	for name, want := range map[string]bool{
		"group:ui":           true,
		"group:main-changes": true,
		"group:a.b_c:d":      true,
		"group:":             false,
		"group":              false,
		"group:ui/web":       false, // a slash parts a device from its session in an address
		"groups:ui":          false,
		"model:opus-5.5":     false,
		"Group:ui":           false,
		"group:UI":           false,
	} {
		if got := IsGroup(name); got != want {
			t.Errorf("IsGroup(%q) = %v; want %v", name, got, want)
		}
	}
}

func TestOnlyGroupsAreGiven(t *testing.T) {
	st := &State{Self: []string{"reviewing"}}
	if _, _, err := st.Given([]string{"Group:UI", "group:bus"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"group:bus", "group:ui", "reviewing"}; !slices.Equal(st.Self, want) {
		t.Errorf("Self is %q; want %q", st.Self, want)
	}
	// The session can take a group it was given off again, as any tag of its own.
	if err := st.SetSelf(nil, []string{"group:ui"}); err != nil || slices.Contains(st.Self, "group:ui") {
		t.Errorf("after removing group:ui: %q, %v", st.Self, err)
	}
	for _, bad := range [][]string{{DoNotDisturb}, {"model:opus-5.5"}, {"group:ui", "reviewing"}, {"group:"}, {Human}} {
		before := slices.Clone(st.Self)
		if _, _, err := st.Given(bad); err == nil {
			t.Errorf("Given(%q) was allowed", bad)
		}
		if !slices.Equal(st.Self, before) {
			t.Errorf("a refused Given(%q) changed Self to %q", bad, st.Self)
		}
	}
	// A human's mark stands over what another agent gives: a group a human took the session out
	// of is left out, and takes no place under MaxPerSession (#543).
	st = &State{Human: map[string]Mark{"group:ui": {On: false, At: time.Now()}}}
	given, out, err := st.Given([]string{"group:ui", "group:bus"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(given, []string{"group:bus"}) || !slices.Equal(out, []string{"group:ui"}) {
		t.Errorf("given %q, out %q; want group:bus given and group:ui out", given, out)
	}
	if !slices.Equal(st.Self, []string{"group:bus"}) {
		t.Errorf("Self is %q; want only group:bus, as a human took the session out of group:ui", st.Self)
	}
	if got := Effective(st.Self, nil, st.Human); Has(got, "group:ui") {
		t.Errorf("the session has %v; want group:ui kept off, as a human took it off", got)
	}
}

// #539. A session leaves a group a human put it in; a human's add from before then, read late,
// doesn't put it back, and one made after does. A human's other marks, and one taking the session
// out of a group, still stand.
func TestASessionLeavesAGroupAHumanPutItIn(t *testing.T) {
	added := time.Now().Add(-time.Hour)
	st := &State{}
	if err := st.SetHuman(nil, []string{"group:ui", "reviewing"}, nil, "tyler", added); err != nil {
		t.Fatal(err)
	}
	st.Human["group:old"] = Mark{On: false, By: "tyler", At: added}
	// A mark dated ahead, on a group left first, sets that group's time and no other's.
	ahead := time.Now().Add(time.Hour)
	st.Human["group:a"] = Mark{On: true, By: "tyler", At: ahead}
	st.Cleared = map[string]time.Time{"other": added}
	kept := *st // shares its maps, as the daemon's copy of the kept State does

	left := time.Now()
	if got := st.Left([]string{"group:a", "group:ui", "reviewing", "group:old", "group:none"}, left); !slices.Equal(got, []string{"group:a", "group:ui"}) {
		t.Errorf("Left took marks off %q; want group:a and group:ui", got)
	}
	if !kept.Human["group:ui"].On || len(kept.Cleared) != 1 {
		t.Error("Left changed the State it was copied from")
	}
	if !st.Cleared["group:a"].Equal(ahead) || !st.Cleared["group:ui"].Equal(left) {
		t.Errorf("the leaves are dated %v; want group:a at its mark's time and group:ui at the leave", st.Cleared)
	}
	if got := Names(Effective(nil, nil, st.Human)); !slices.Equal(got, []string{"reviewing"}) {
		t.Errorf("the tags are %q; want reviewing alone", got)
	}
	if st.Human["group:old"].On || st.Human["group:old"].At.IsZero() {
		t.Error("the human's mark taking the session out of group:old went")
	}

	if err := st.SetHuman(nil, []string{"group:ui"}, nil, "maya", left.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if Has(Effective(nil, nil, st.Human), "group:ui") {
		t.Error("a human's add from before the session left, read late, put it back")
	}
	if err := st.SetHuman(nil, []string{"group:ui"}, nil, "maya", left.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !Has(Effective(nil, nil, st.Human), "group:ui") {
		t.Error("a human's add after the session left didn't put it back")
	}
}

// A device's group (#463) is Murmur's to give: nobody sets or removes it, a presence can't claim
// one, and a reader adds the one for the device a session is on.
func TestADevicesGroupIsGivenOnlyByWhereTheSessionRuns(t *testing.T) {
	if _, err := Clean([]string{"group:device-laptop-1a2b"}); err == nil {
		t.Error("Clean took a device's group")
	}
	if _, err := Groups([]string{"group:device-laptop-1a2b"}); err == nil {
		t.Error("Groups took a device's group, to add to")
	}
	read := CleanRead([]Tag{{Name: "reviewing"}, {Name: "group:device-other-9f9f", Src: SrcDerived}})
	if !slices.Equal(Names(read), []string{"reviewing"}) {
		t.Errorf("a presence's claim was read: %v", read)
	}
	got := WithDevice(read, "laptop-1a2b")
	if !slices.Equal(Names(got), []string{"reviewing", "group:device-laptop-1a2b"}) || got[1].Src != SrcDerived {
		t.Errorf("WithDevice gave %+v", got)
	}
	if again := WithDevice(got, "laptop-1a2b"); len(again) != 2 {
		t.Errorf("a second WithDevice gave %+v", again)
	}
	if got := DevicesOfGroup(" Group:Device-Laptop-1A2B "); !slices.Equal(got, []string{"device-laptop-1a2b", "laptop-1a2b"}) {
		t.Errorf("DevicesOfGroup: %q", got)
	}
	if got := DevicesOfGroup("group:ui"); got != nil {
		t.Errorf("group:ui read as a device's group: %q", got)
	}
	// An ID made since #330 starts with device- already.
	if g := DeviceGroup("device-3f2a9c1e"); g != "group:device-3f2a9c1e" || !slices.Contains(DevicesOfGroup(g), "device-3f2a9c1e") {
		t.Errorf("DeviceGroup(device-3f2a9c1e) = %q", g)
	}
}

// A device ID can be 40 long, so its group's tag can pass MaxLen, and who still searches by it
// (Sol's review of #628).
func TestALongDevicesGroupIsSearchable(t *testing.T) {
	long := DeviceGroup("abcdefghijklmnopqrstuvwx-1234")
	if len(long) <= MaxLen {
		t.Fatalf("%s is no longer than %d", long, MaxLen)
	}
	ts := WithDevice([]Tag{{Name: "reviewing"}}, "abcdefghijklmnopqrstuvwx-1234")
	for _, terms := range [][]string{{long}, {long[:len(long)-2] + "*"}, {"group:device-*"}} {
		f, err := ParseFilter(terms)
		if err != nil {
			t.Fatalf("%q: %v", terms, err)
		}
		if !f.Match(ts) {
			t.Errorf("%q doesn't match %v", terms, Names(ts))
		}
	}
	f, err := ParseFilter([]string{"-" + long})
	if err != nil || f.Match(ts) {
		t.Errorf("-%s: %v, matches %v", long, err, f.Match(ts))
	}
}

// A group a team made under the prefix devices now hold (group:device-lab) is refused with why,
// and the name to use instead: the same group without device- (#705).
func TestOwnGroupHint(t *testing.T) {
	for name, want := range map[string]string{
		"group:device-lab":        " If it was a group your team made, names that start with group:device- are devices' now: use group:lab.",
		"GROUP:Device-Lab":        " If it was a group your team made, names that start with group:device- are devices' now: use group:lab.",
		"group:device-device-lab": "", // what it would suggest is a device's group too
		"group:device-":           "",
		"group:lab":               "",
	} {
		if got := OwnGroupHint(name); got != want {
			t.Errorf("OwnGroupHint(%q) = %q, want %q", name, got, want)
		}
	}
	if _, err := Clean([]string{"group:device-lab"}); err == nil || !strings.Contains(err.Error(), "use group:lab") {
		t.Errorf("Clean of a group under the device prefix: %v", err)
	}
}
