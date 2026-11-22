package web

import (
	"context"
	"net/http"
)

// Updates reads and changes how Murmur updates itself on this machine (#1045). The daemon's is
// update.Read, update.Set and update.Back over the global git config.
type Updates interface {
	UpdateSetting(ctx context.Context) (UpdateSetting, error)
	// SetUpdates makes choice (UpdatesOff, UpdatesOn or UpdatesNightly) the machine's.
	SetUpdates(ctx context.Context, choice string) (UpdateSetting, error)
	// BackToStable installs the latest stable release in place of the prerelease running, and
	// holds updates until a newer release than that prerelease is out.
	BackToStable(ctx context.Context) (UpdateSetting, error)
}

const (
	UpdatesOff     = "off"
	UpdatesOn      = "on"
	UpdatesNightly = "nightly"
)

// UpdateSetting is what the page's Updates row shows.
type UpdateSetting struct {
	// Choice is the machine's: off, on or nightly.
	Choice string `json:"choice"`
	// Effective is what applies in this clone, which its own git config or MURMUR_NO_UPDATE can
	// make differ from Choice; Why then says which, in words to show.
	Effective string `json:"effective"`
	Why       string `json:"why,omitempty"`
	// Hold says, in words to show, that updates wait for a release newer than one gone back from.
	Hold string `json:"hold,omitempty"`
	// CanGoBack says BackToStable would do something: a prerelease runs and updates are on here.
	CanGoBack bool `json:"canGoBack,omitempty"`
}

// WithUpdates lets the page show and change the update setting. Without it, as in tests, the
// page shows none.
func (s *Server) WithUpdates(u Updates) { s.updates = u }

type updatesRequest struct {
	Choice string `json:"choice"`
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeError(w, http.StatusNotFound, "updates can't be set from this page")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	u, err := s.updates.UpdateSetting(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, u)
}

func (s *Server) putUpdates(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeError(w, http.StatusNotFound, "updates can't be set from this page")
		return
	}
	var req updatesRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Choice {
	case UpdatesOff, UpdatesOn, UpdatesNightly:
	default:
		writeError(w, http.StatusBadRequest, "choice: must be off, on or nightly")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	u, err := s.updates.SetUpdates(ctx, req.Choice)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "couldn't change updates: "+err.Error())
		return
	}
	writeJSON(w, u)
}

func (s *Server) postBackToStable(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeError(w, http.StatusNotFound, "updates can't be set from this page")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	u, err := s.updates.BackToStable(ctx)
	if err != nil {
		writeError(w, http.StatusConflict, "couldn't go back to stable: "+err.Error())
		return
	}
	writeJSON(w, u)
}
