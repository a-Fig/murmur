package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
)

// Settings for all repos (#816, Tyler, 2026-11-16: "changes on that page make everything comply").
// Each clone's daemon keeps its own settings, so a choice made on the page's All repos tab is written
// to one record for the machine, in the clones directory beside the clones' own records, and every
// daemon that reads a newer write than it has taken copies what that write set into its own
// settings. A clone that joins later takes everything the record holds when its daemon first runs.
// A repo's own page can still change a setting afterwards, for that repo alone, and it stays so
// until the next All repos change of that setting.
//
// Only what is about the person's own sessions and files goes here: the wake and busy defaults and
// the file switches. A session's own choices are its repo's, as the session is; availability is
// for one repo at a time; the account, nick and color are what one mesh's teammates see.

// AllReposFile is the record's name in the clones directory.
const AllReposFile = "all-repos.json"

// AllRepos are the settings someone set for every repo. A nil one was never set that way, and
// each repo keeps its own.
type AllRepos struct {
	Wake             *Wake   `json:"wake,omitempty"`
	Busy             *string `json:"busy,omitempty"`
	SendFiles        *bool   `json:"sendFiles,omitempty"`
	ReceiveFiles     *bool   `json:"receiveFiles,omitempty"`
	AgentsSendOff    *bool   `json:"agentsSendOff,omitempty"`
	AgentsReceiveOff *bool   `json:"agentsReceiveOff,omitempty"`
}

// AllReposRecord is the file.
type AllReposRecord struct {
	// ID is made with the file, so a daemon that took writes of a file since deleted takes a new
	// file's from its first: their count starts again.
	ID string `json:"id"`
	// Seq counts the writes. A file's time can't tell one write from the next on a file system
	// that keeps it to the second.
	Seq      int64    `json:"seq"`
	Settings AllRepos `json:"settings"`
	// Set is the write that last set each setting, by its JSON name.
	Set map[string]int64 `json:"set,omitempty"`
}

// check refuses a value the repo's page wouldn't save.
func (a AllRepos) check() error {
	if a.Wake != nil && !a.Wake.valid() {
		return errors.New("wake: when must be always, within or never, and minutes 0 to 1440, given only with within")
	}
	if a.Busy != nil && !validBusy(*a.Busy) {
		return errors.New("busy: must be subject or whole")
	}
	return nil
}

// allField is one of AllRepos's settings: its JSON name, whether it is set, and a way to copy it.
type allField struct {
	name  string
	has   bool
	apply func()
}

// fields are a's settings, copied into s by their apply.
func (a *AllRepos) fields(s *Settings) []allField {
	return []allField{
		{"wake", a.Wake != nil, func() { s.Wake = *a.Wake }},
		{"busy", a.Busy != nil, func() { s.Busy = *a.Busy }},
		{"sendFiles", a.SendFiles != nil, func() { s.SendFiles = *a.SendFiles }},
		{"receiveFiles", a.ReceiveFiles != nil, func() { s.ReceiveFiles = *a.ReceiveFiles }},
		{"agentsSendOff", a.AgentsSendOff != nil, func() { s.AgentsSendOff = *a.AgentsSendOff }},
		{"agentsReceiveOff", a.AgentsReceiveOff != nil, func() { s.AgentsReceiveOff = *a.AgentsReceiveOff }},
	}
}

// Since copies into s what the record set after write seq of the record id (every setting it holds
// when id is another file's), and says whether it copied anything. FilesSet names the file
// switches it copied, as a save from the page does, so the daemon takes those and no others.
func (r AllReposRecord) Since(s *Settings, id string, seq int64) bool {
	if r.ID != id {
		seq = 0
	}
	s.FilesSet = nil
	any := false
	for _, f := range r.Settings.fields(s) {
		if !f.has || r.Set[f.name] <= seq {
			continue
		}
		f.apply()
		any = true
		if f.name != "wake" && f.name != "busy" {
			s.FilesSet = append(s.FilesSet, f.name)
		}
	}
	return any
}

// ReadAllRepos reads the record at path; one that isn't there, or isn't one, is empty.
func ReadAllRepos(path string) (AllReposRecord, error) {
	var r AllReposRecord
	var b []byte
	err := retryShared(func() (err error) {
		b, err = os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			b = nil
			return nil
		}
		return err
	})
	if err != nil || b == nil {
		return r, err
	}
	if json.Unmarshal(b, &r) != nil || r.Settings.check() != nil {
		return AllReposRecord{}, nil // a file in a shape this doesn't know sets nothing
	}
	return r, nil
}

// SetAllRepos sets every setting change has, for all repos, and returns the record as written.
// Reading the record and writing it back is one step across the machine's daemons.
func SetAllRepos(path string, change AllRepos) (AllReposRecord, error) {
	if err := change.check(); err != nil {
		return AllReposRecord{}, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AllReposRecord{}, err
	}
	var lock *proc.Held
	if err := retryShared(func() (err error) {
		lock, err = proc.Lock(path + ".lock")
		return err
	}); err != nil {
		return AllReposRecord{}, err
	}
	defer lock.Close()
	r, err := ReadAllRepos(path)
	if err != nil {
		return AllReposRecord{}, err
	}
	if r.ID == "" {
		b := make([]byte, 8)
		rand.Read(b)
		r = AllReposRecord{ID: hex.EncodeToString(b)}
	}
	if r.Set == nil {
		r.Set = map[string]int64{}
	}
	r.Seq++
	var none Settings
	for _, f := range change.fields(&none) {
		if f.has {
			r.Set[f.name] = r.Seq
		}
	}
	merge(&r.Settings, change)
	b, err := json.Marshal(r)
	if err != nil {
		return AllReposRecord{}, err
	}
	f, err := os.CreateTemp(dir, ".all-repos-*")
	if err != nil {
		return AllReposRecord{}, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = retryShared(func() error { return os.Rename(f.Name(), path) })
	}
	if err != nil {
		return AllReposRecord{}, err
	}
	return r, nil
}

func merge(into *AllRepos, change AllRepos) {
	if change.Wake != nil {
		into.Wake = change.Wake
	}
	if change.Busy != nil {
		into.Busy = change.Busy
	}
	if change.SendFiles != nil {
		into.SendFiles = change.SendFiles
	}
	if change.ReceiveFiles != nil {
		into.ReceiveFiles = change.ReceiveFiles
	}
	if change.AgentsSendOff != nil {
		into.AgentsSendOff = change.AgentsSendOff
	}
	if change.AgentsReceiveOff != nil {
		into.AgentsReceiveOff = change.AgentsReceiveOff
	}
}

// retryShared gives another daemon time to release the lock and, on Windows, to finish reading the
// file: every daemon reads it every few seconds, and Windows refuses to rename over a file that
// any process has open.
func retryShared(op func() error) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := op()
		if err == nil || !time.Now().Before(deadline) || runtime.GOOS != "windows" && !errors.Is(err, proc.ErrLocked) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WithAllRepos lets the page set settings for all repos, in the record at path. changed is called
// after each write, so this clone's daemon takes it at once rather than at its next look.
func (s *Server) WithAllRepos(path string, changed func()) *Server {
	s.allRepos, s.allReposChanged = path, changed
	return s
}

func (s *Server) getAllRepos(w http.ResponseWriter, r *http.Request) {
	if s.allRepos == "" {
		writeError(w, http.StatusNotFound, "settings for all repos aren't kept here")
		return
	}
	rec, err := ReadAllRepos(s.allRepos)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, rec.Settings)
}

func (s *Server) putAllRepos(w http.ResponseWriter, r *http.Request) {
	if s.allRepos == "" {
		writeError(w, http.StatusNotFound, "settings for all repos aren't kept here")
		return
	}
	var change AllRepos
	if err := readJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := change.check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, err := SetAllRepos(s.allRepos, change)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "saving settings for all repos: "+err.Error())
		return
	}
	if s.allReposChanged != nil {
		s.allReposChanged()
	}
	writeJSON(w, rec.Settings)
}
