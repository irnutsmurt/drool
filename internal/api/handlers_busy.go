package api

import (
	"net/http"
	"sort"
	"strings"
)

// What is using a host right now (1.0 UI/UX correction): the holders of
// its host lock, in words, so a refused action can say what is in the way
// and the page can offer it again once that has finished. Advisory only:
// every action takes the lock again when it starts, and is refused as
// before if it can't.

type busyHolderJSON struct {
	Kind  string `json:"kind"` // run, refresh, status_check, preview, dry_run, files, removal, shepherd_install, shepherd_uninstall, other
	Label string `json:"label"`
	JobID string `json:"job_id,omitempty"`
}

type hostBusyJSON struct {
	Busy    bool             `json:"busy"`
	Holders []busyHolderJSON `json:"holders"`
}

// lockHolder names one lock owner (internal/engine/lock: a run's job id, or
// what an API handler or the poller called its use).
func lockHolder(owner string) busyHolderJSON {
	switch {
	case strings.HasPrefix(owner, "job_"):
		return busyHolderJSON{Kind: "run", Label: "A run", JobID: owner}
	case owner == "refresh":
		return busyHolderJSON{Kind: "refresh", Label: "Collecting the host's inventory and status (Refresh)"}
	case owner == "poller":
		return busyHolderJSON{Kind: "status_check", Label: "A routine status check (a few seconds)"}
	case owner == "preview":
		return busyHolderJSON{Kind: "preview", Label: "A file deployment preview"}
	case owner == "simulate":
		return busyHolderJSON{Kind: "dry_run", Label: "A package dry run"}
	case strings.HasPrefix(owner, "files:"):
		return busyHolderJSON{Kind: "files", Label: "A file change (Files)"}
	case owner == "host-removal":
		return busyHolderJSON{Kind: "removal", Label: "Removing the host"}
	case owner == "agent-install":
		return busyHolderJSON{Kind: "shepherd_install", Label: "Installing a Shepherd"}
	case owner == "agent-uninstall":
		return busyHolderJSON{Kind: "shepherd_uninstall", Label: "Uninstalling the Shepherd"}
	}
	return busyHolderJSON{Kind: "other", Label: "Another operation"}
}

func (s *Server) handleHostBusy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.d.Inventory.GetHost(r.Context(), id); err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	owners := s.d.Engine.Locks().Holders(id)
	sort.Strings(owners)
	out := hostBusyJSON{Busy: len(owners) > 0, Holders: []busyHolderJSON{}}
	for _, o := range owners {
		h := lockHolder(o)
		if h.JobID != "" {
			if j, err := s.d.Store.R.GetJob(r.Context(), h.JobID); err == nil {
				h.Label = "The run “" + j.Name + "”"
			}
		}
		out.Holders = append(out.Holders, h)
	}
	writeJSON(w, http.StatusOK, out)
}
