package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/tags"
)

// A tag change on a session keeps where each of its tags came from: one its daemon worked out
// stays derived rather than becoming the session's own (#224).
func TestSetTagsKeepsDerivedTags(t *testing.T) {
	f := newFixtures(time.Now())
	if err := f.SetTags(context.Background(), "quiet-otter", []string{"db"}, nil); err != nil {
		t.Fatal(err)
	}
	var got []tags.Tag
	for _, d := range f.devices {
		for _, s := range d.Sessions {
			if s.Name == "quiet-otter" {
				got = s.Tags
			}
		}
	}
	want := []tags.Tag{
		{Name: "db", Src: tags.SrcHuman},
		{Name: "model:opus-5.5", Src: tags.SrcDerived},
		{Name: "ui", Src: tags.SrcSession},
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("tags are %+v, want %+v among them", got, w)
		}
	}
}
