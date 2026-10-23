package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"drool/internal/auth"
	"drool/internal/desired"
	"drool/internal/monitor/monsvc"
)

// packConflict is one place where a Pack's setting doesn't decide what a
// member host gets (ARCHITECTURE-ALPHA §47): Packs of equal priority
// setting it differently (a conflict: DROOL picks neither), or a Pack of
// higher priority setting it differently (this Pack is outranked there).
// The services decide; this only gathers their verdicts for one Pack.
type packConflict struct {
	Area     string            `json:"area"` // monitoring | desired
	Kind     string            `json:"kind"` // conflict | outranked
	HostID   string            `json:"host_id"`
	HostName string            `json:"host_name"`
	What     string            `json:"what"`
	Check    string            `json:"check,omitempty"`
	Field    string            `json:"field,omitempty"`
	Key      string            `json:"key,omitempty"`
	Value    string            `json:"value,omitempty"` // this Pack's value, in words (monitoring)
	Others   []packConflictRef `json:"others"`
	Applies  string            `json:"applies"`
}

type packConflictRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Value    string `json:"value,omitempty"`
}

type packConflictsJSON struct {
	Items []packConflict `json:"items"`
	// Checked: the areas read — those the role may see and that run here.
	Checked []string `json:"checked"`
}

// handleGroupConflicts lists, for each host in the Pack, the health check
// fields and Desired State resources where this Pack's setting doesn't
// apply because of another Pack. A host's own setting decides for that
// host and isn't listed.
func (s *Server) handleGroupConflicts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	g, err := s.d.Inventory.GetGroup(ctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	names := map[string]string{}
	for _, h := range hosts {
		names[h.ID] = h.Name
	}
	p := principalFrom(ctx)
	out := packConflictsJSON{Items: []packConflict{}, Checked: []string{}}

	if s.d.Monitor != nil && p.Can(auth.PermMonitoringRead) {
		out.Checked = append(out.Checked, "monitoring")
		gv, err := s.d.Monitor.GroupView(ctx, id)
		if err != nil {
			writeProblem(w, r, errInternal)
			return
		}
		// What this Pack sets itself (check → field → value).
		sets := map[string]map[string]*float64{}
		for _, c := range gv.Checks {
			for _, f := range c.Fields {
				if f.Own != nil {
					if sets[c.Check] == nil {
						sets[c.Check] = map[string]*float64{}
					}
					sets[c.Check][f.Name] = f.Own
				}
			}
		}
		for _, hid := range g.HostIDs {
			v, err := s.d.Monitor.HostView(ctx, hid, "", "")
			if err != nil {
				continue // the host went away meanwhile
			}
			for _, c := range v.Checks {
				for _, f := range c.Fields {
					if f.Own != nil {
						continue // the host's own setting decides here
					}
					in := f.Inherited
					item := packConflict{Area: "monitoring", HostID: hid, HostName: names[hid], What: c.Label + ", " + strings.ToLower(f.Label),
						Check: c.Check, Field: f.Name, Others: []packConflictRef{},
						Applies: fieldWords(in.Value, f.FieldSpec) + " from " + provenanceWords(in.Source)}
					switch {
					case in.Conflict != nil && hasGroup(in.Conflict.Groups, id):
						item.Kind = "conflict"
						for _, o := range in.Conflict.Groups {
							if o.ID == id {
								item.Value = fieldWords(o.Value, f.FieldSpec)
								continue
							}
							item.Others = append(item.Others, packConflictRef{ID: o.ID, Name: o.Name, Priority: o.Priority, Value: fieldWords(o.Value, f.FieldSpec)})
						}
					case sets[c.Check][f.Name] != nil && in.Source.Kind == "group" && !hasGroup(in.Source.Groups, id):
						item.Kind = "outranked"
						item.Value = fieldWords(sets[c.Check][f.Name], f.FieldSpec)
						for _, o := range in.Source.Groups {
							item.Others = append(item.Others, packConflictRef{ID: o.ID, Name: o.Name, Priority: o.Priority, Value: fieldWords(in.Value, f.FieldSpec)})
						}
					default:
						continue
					}
					out.Items = append(out.Items, item)
				}
			}
		}
	}

	if s.d.Desired != nil && p.Can(auth.PermDesiredRead) {
		out.Checked = append(out.Checked, "desired")
		for _, hid := range g.HostIDs {
			v, err := s.d.Desired.Host(ctx, hid)
			if err != nil {
				continue
			}
			for _, res := range v.Resources {
				if res.Status != desired.StatusConflict || !hasDesiredGroup(res.Source.Groups, id) {
					continue
				}
				item := packConflict{Area: "desired", Kind: "conflict", HostID: hid, HostName: names[hid], What: strings.TrimSpace(res.Kind + " " + res.Name),
					Key: res.Key, Others: []packConflictRef{}, Applies: "neither: it isn't checked or restored on this host until it's settled"}
				for _, o := range res.Source.Groups {
					if o.ID != id {
						item.Others = append(item.Others, packConflictRef{ID: o.ID, Name: o.Name, Priority: o.Priority})
					}
				}
				out.Items = append(out.Items, item)
			}
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.HostName != b.HostName {
			return a.HostName < b.HostName
		}
		if a.Area != b.Area {
			return a.Area < b.Area
		}
		return a.What < b.What
	})
	writeJSON(w, http.StatusOK, out)
}

func hasGroup(gs []monsvc.GroupRef, id string) bool {
	for _, g := range gs {
		if g.ID == id {
			return true
		}
	}
	return false
}

func hasDesiredGroup(gs []desired.GroupRef, id string) bool {
	for _, g := range gs {
		if g.ID == id {
			return true
		}
	}
	return false
}

// fieldWords is a health field's value as the UI says it ("90%", "on",
// "10 min", "none").
func fieldWords(v *float64, f monsvc.FieldSpec) string {
	if v == nil {
		return "none"
	}
	if f.Type == "bool" {
		if *v != 0 {
			return "on"
		}
		return "off"
	}
	n := strconv.FormatFloat(*v, 'f', -1, 64)
	switch f.Unit {
	case "":
		return n
	case "%":
		return n + "%"
	default:
		return fmt.Sprintf("%s %s", n, f.Unit)
	}
}

// provenanceWords names a value's source as the UI does.
func provenanceWords(p monsvc.Provenance) string {
	switch p.Kind {
	case "recommended":
		return "Recommended"
	case "global":
		return "every host's setting"
	case "hardware":
		return "the hardware's limit"
	case "group":
		ns := make([]string, 0, len(p.Groups))
		for _, g := range p.Groups {
			ns = append(ns, g.Name)
		}
		return "the Pack " + strings.Join(ns, ", ")
	}
	return p.Label
}
