package daemon

import (
	"context"
	"path/filepath"
	"time"

	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/web"
)

// Settings for all repos (#816): the page's All repos tab writes them to one record for the
// machine (clones.AllReposFile), and each daemon takes a write newer than
// the last it took into its own settings, as if its own page had saved them. web/allrepos.go has
// the whole of it.

// allReposEvery is how often a daemon looks at the record. The daemon whose page made the change
// takes it at once (takeAllRepos), and the others within this.
var allReposEvery = 5 * time.Second

func (d *Daemon) allReposPath() string { return filepath.Join(d.opt.Clones, clones.AllReposFile) }

func (d *Daemon) allReposLoop(ctx context.Context) {
	t := time.NewTicker(allReposEvery)
	defer t.Stop()
	var said string // the last failure logged, so one that lasts is logged once
	for {
		// At the start too: a clone that joined since takes what is there.
		if err := d.takeAllRepos(); err != nil && err.Error() != said {
			d.log.Printf("taking the settings for all repos: %v", err)
			said = err.Error()
		} else if err == nil {
			said = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// takeAllRepos copies into this clone's settings what the record set since the write they last
// took, and saves them.
func (d *Daemon) takeAllRepos() error {
	rec, err := web.ReadAllRepos(d.allReposPath())
	if err != nil || rec.ID == "" {
		return err
	}
	d.setMu.Lock()
	defer d.setMu.Unlock()
	cur := d.settings
	if rec.ID == cur.AllID && rec.Seq <= cur.AllSeq {
		return nil
	}
	set := cur.view()
	if !rec.Since(&set, cur.AllID, cur.AllSeq) {
		set.FilesSet = nil // nothing new: only the write is noted
	}
	_, err = d.setSettingsLocked(set, rec.ID, rec.Seq)
	return err
}
