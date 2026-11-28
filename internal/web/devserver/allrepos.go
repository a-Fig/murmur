package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/a-Fig/murmur/internal/web"
)

// withAllRepos gives every page the All repos tab (#816), with a record of its own in a temporary
// folder. Every made-up clone takes a change at once, where a daemon would within a few seconds.
func withAllRepos(srcs []*fixtures, pages []*web.Server) {
	dir, err := os.MkdirTemp("", "murmur-dev-all-repos-")
	if err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(dir, web.AllReposFile)
	take := func() {
		rec, err := web.ReadAllRepos(path)
		if err != nil {
			log.Printf("reading the settings for all repos: %v", err)
			return
		}
		for _, f := range srcs {
			f.takeAllRepos(rec)
		}
	}
	for _, p := range pages {
		p.WithAllRepos(path, take)
	}
}

func (f *fixtures) takeAllRepos(rec web.AllReposRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec.ID == "" || rec.ID == f.allID && rec.Seq <= f.allSeq {
		return
	}
	set := copySettings(f.settings)
	rec.Since(&set, f.allID, f.allSeq)
	set.FilesSet = nil
	f.settings, f.allID, f.allSeq = set, rec.ID, rec.Seq
}
