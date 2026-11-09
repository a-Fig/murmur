package daemon

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/tags"
)

// #251. A human on another device takes do-not-disturb off a session while that device's log can't
// be fetched, and a human on this device's page then puts it back. When the fetch lands, the older
// change doesn't take it off again.
func TestATagChangeReadLateLeavesANewerOne(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	read := r.append("", r.message("read before"))
	r.sync()
	r.append(read, r.tagsEvent(time.Now().Add(-30*time.Minute), "do-not-disturb"))
	recovered := r.failFetches()
	r.sync()

	if err := (pageSource{r.d}).SetTags(r.ctx, bus.Addr(r.d.device, "bbbbbb"), []string{"do-not-disturb"}, nil); err != nil {
		t.Fatal(err)
	}
	recovered()
	r.sync()
	if r.off("do-not-disturb") {
		t.Error("the change read late overwrote the newer one")
	}
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	if !r.d.dndLocked("bbbbbb") {
		t.Error("the session isn't on do not disturb")
	}
}

// A mark dated in the future can already be in state.json, from before times were capped or from a
// clock since corrected. It can't make the page ignore a click, and a click on another tag doesn't
// take on its date, which would hold off every change from another device to that tag until then.
func TestAMarkFromTheFutureHoldsNothingOff(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	addr := bus.Addr(r.d.device, "bbbbbb")
	tomorrow := time.Now().Add(24 * time.Hour).UTC()
	r.d.mu.Lock()
	r.d.st.Tags["quiet-otter"].Human = map[string]tags.Mark{
		"do-not-disturb": {On: false, By: "maya", At: tomorrow},
		"release":        {On: true, By: "maya", At: tomorrow},
	}
	r.d.mu.Unlock()
	if err := (pageSource{r.d}).SetTags(r.ctx, addr, []string{"urgent"}, nil); err != nil {
		t.Fatal(err)
	}
	r.d.mu.Lock()
	r.d.applyTagsEventLocked(r.tagsEvent(time.Now().Add(-time.Minute), "urgent"))
	_, urgent := r.d.st.Tags["quiet-otter"].Human["urgent"]
	r.d.mu.Unlock()
	if urgent {
		t.Error("the click took on a future date, and held off a change from another device")
	}

	if err := (pageSource{r.d}).SetTags(r.ctx, addr, []string{"do-not-disturb"}, nil); err != nil {
		t.Fatal(err)
	}
	if r.off("do-not-disturb") {
		t.Error("the click didn't put the tag back")
	}
	if err := (pageSource{r.d}).SetTags(r.ctx, addr, nil, []string{"do-not-disturb"}); err != nil {
		t.Fatal(err)
	}
	if !r.off("do-not-disturb") {
		t.Error("the next click didn't take it off")
	}
}

// A change refused for being older than a mark from the future still brings that mark back to now
// for good. Otherwise the mark goes on refusing what was sent over a minute before it is read, since
// its date is capped afresh, at a later now, each time.
func TestARefusedChangeStillCapsAMarkFromTheFuture(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.d.st.Tags["quiet-otter"].Human = map[string]tags.Mark{
		"do-not-disturb": {On: true, By: "maya", At: time.Now().Add(24 * time.Hour).UTC()},
	}
	r.d.applyTagsEventLocked(r.tagsEvent(time.Now().Add(-time.Hour), "do-not-disturb"))
	m := r.d.st.Tags["quiet-otter"].Human["do-not-disturb"]
	if !m.On {
		t.Fatal("the change older than the mark was applied")
	}
	if m.At.After(time.Now()) {
		t.Errorf("the mark is still dated %s, in the future", m.At)
	}
}

// A tags event dated in the future, by a clock ahead or a forged commit, is taken as sent now, so it
// can't hold off every later change until that time comes.
func TestATagChangeFromTheFutureCountsAsNow(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	remove := r.tagsEvent(time.Now().Add(24*time.Hour), "do-not-disturb")
	r.d.applyTagsEventLocked(remove)
	if m := r.d.st.Tags["quiet-otter"].Human["do-not-disturb"]; m.At.After(time.Now()) {
		t.Fatalf("the mark is dated %s, in the future", m.At)
	}
	add := r.tagsEvent(time.Now().Add(2*time.Minute), "x")
	add.Remove, add.Add = nil, []string{"do-not-disturb"}
	r.d.applyTagsEventLocked(add)
	if !r.d.dndLocked("bbbbbb") {
		t.Error("a later change was held off by one dated in the future")
	}
}
