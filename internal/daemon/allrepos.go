package daemon

import (
	"context"
	"path/filepath"
	"time"

	"github.com/a-Fig/murmur/internal/web"
)

// Settings for all repos (#816): the page's All repos tab writes them to one record for the
// machine (web.AllReposFile, in the clones directory), and each daemon takes a write newer than
// the last it took into its own settings, as if its own page had saved them. web/allrepos.go has
// the whole of it.

// allReposEvery is how often a daemon looks at the record. The daemon whose page made the change
// takes it at once (takeAllRepos), and the others within this.
var allReposEvery = 5 * time.Second

func (d *Daemon) allReposPath() string { return filepath.Join(d.opt.Clones, web.AllReposFile) }

func (d *Daemon) allReposLoop(ctx context.Context) {
	t := time.NewTicker(allReposEvery)
	defer t.Stop()
	for {
		d.takeAllRepos() // at the start too: a clone that joined since takes what is there
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// takeAllRepos copies into this clone's settings what the record set since the write they last
// took, and saves them.
func (d *Daemon) takeAllRepos() {
	rec, err := web.ReadAllRepos(d.allReposPath())
	if err != nil {
		d.log.Printf("reading the settings for all repos: %v", err)
		return
	}
	if rec.ID == "" {
		return
	}
	d.setMu.Lock()
	defer d.setMu.Unlock()
	cur := d.settings
	if rec.ID == cur.AllID && rec.Seq <= cur.AllSeq {
		return
	}
	set := cur.view()
	if !rec.Since(&set, cur.AllID, cur.AllSeq) {
		set.FilesSet = nil // nothing new: only the write is noted
	}
	if _, err := d.setSettingsLocked(set, rec.ID, rec.Seq); err != nil {
		d.log.Printf("taking the settings for all repos: %v", err)
	}
}
