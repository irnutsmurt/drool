package desired

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"drool/internal/artifacts"
	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/model"
)

func previewFile(ctx context.Context, fsys files.FS, src *artifacts.Store, op *model.FileDeployOp, content bool) (*filedeploy.PreviewResult, error) {
	if content && src != nil {
		return filedeploy.Preview(ctx, fsys, src, op)
	}
	return filedeploy.PreviewMeta(ctx, fsys, op)
}

// JobWarning says that an ordinary job would change something desired
// state manages the other way (it is a warning, not a refusal).
type JobWarning struct {
	Step    int      `json:"step"` // 0-based
	Key     string   `json:"key"`
	Hosts   []string `json:"hosts"` // host names
	Message string   `json:"message"`
	// Auto: automatic restore would put it back after the job.
	Auto bool `json:"auto"`
}

// JobWarnings checks a job's typed steps against the targets' effective
// desired state: stopping a service that should run, installing a
// prohibited package, deploying other content to a managed file, …
func (s *Service) JobWarnings(ctx context.Context, hostIDs []string, steps []model.Operation) []JobWarning {
	s.fresh(ctx)
	s.mu.Lock()
	snap, eff := s.snap, s.eff
	s.mu.Unlock()
	if snap == nil {
		return nil
	}
	type wkey struct {
		step int
		key  string
		msg  string
		auto bool
	}
	hosts := map[wkey][]string{}
	var order []wkey
	add := func(step int, key, msg string, auto bool, host string) {
		k := wkey{step, key, msg, auto}
		if _, ok := hosts[k]; !ok {
			order = append(order, k)
		}
		hosts[k] = append(hosts[k], host)
	}
	for _, hid := range hostIDs {
		hs, ok := eff[hid]
		if !ok {
			continue
		}
		name := snap.Hosts[hid]
		for i, st := range steps {
			switch st.Type {
			case model.OpService:
				if st.Service == nil {
					continue
				}
				key := KindService + ":" + st.Service.UnitName()
				e, ok := hs.Resources[key]
				if !ok || e.Resource == nil {
					continue
				}
				d := e.Resource
				var msg string
				switch {
				case st.Service.State == "stopped" && d.State == StateRunning:
					msg = fmt.Sprintf("%s is managed to keep running (%s). Stopping it will create desired-state drift.", d.Name, e.Source.Label)
				case st.Service.State == "running" && d.State == StateStopped:
					msg = fmt.Sprintf("%s is managed to stay stopped (%s). Starting it will create desired-state drift.", d.Name, e.Source.Label)
				case st.Service.Enabled != nil && d.Enabled != nil && *st.Service.Enabled != *d.Enabled:
					want := map[bool]string{true: "enabled", false: "disabled"}[*d.Enabled]
					msg = fmt.Sprintf("%s is managed to stay %s at boot (%s). This job changes that and will create desired-state drift.", d.Name, want, e.Source.Label)
				}
				if msg != "" {
					add(i, key, msg, e.Source.Auto && e.Source.Level != LevelHost, name)
				}
			case model.OpPackage:
				if st.Package == nil {
					continue
				}
				for _, n := range st.Package.Names {
					key := KindPackage + ":" + n
					e, ok := hs.Resources[key]
					if !ok || e.Resource == nil {
						continue
					}
					var msg string
					switch {
					case st.Package.Action == "install" && e.Resource.State == StateAbsent:
						msg = fmt.Sprintf("%s is prohibited by %s. Installing it will create drift.", n, sourceName(e.Source))
					case st.Package.Action == "remove" && e.Resource.State == StateInstalled:
						msg = fmt.Sprintf("%s is required by %s. Removing it will create drift.", n, sourceName(e.Source))
					}
					if msg != "" {
						add(i, key, msg, e.Source.Auto && e.Source.Level != LevelHost, name)
					}
				}
			case model.OpFileDeploy:
				fd := st.FileDeploy
				if fd == nil || fd.RollbackOf != "" {
					continue
				}
				key := KindFile + ":" + fd.Path
				e, ok := hs.Resources[key]
				if !ok || e.Resource == nil {
					continue
				}
				d := e.Resource
				if fd.Artifact != d.Artifact || d.Mode != "" && fd.Mode != "" && fd.Mode != d.Mode ||
					d.Owner != "" && fd.Owner != "" && fd.Owner != d.Owner || d.Group != "" && fd.Group != "" && fd.Group != d.Group {
					add(i, key, fmt.Sprintf("%s is a managed file (%s) with other content or permissions. Deploying this will create desired-state drift.",
						fd.Path, e.Source.Label), e.Source.Auto && e.Source.Level != LevelHost, name)
				}
			}
		}
	}
	out := make([]JobWarning, 0, len(order))
	for _, k := range order {
		hs := hosts[k]
		sort.Strings(hs)
		msg := hostList(hs) + ": " + k.msg
		if k.auto {
			msg += " Automatic reconciliation may restore this setting after the job completes. Use maintenance or change desired state if this change is intentional."
		}
		out = append(out, JobWarning{Step: k.step, Key: k.key, Hosts: hs, Message: msg, Auto: k.auto})
	}
	return out
}

func sourceName(src Source) string {
	if len(src.Baselines) > 0 {
		return "baseline “" + src.Baselines[0].Name + "”"
	}
	return "this host's desired state"
}

func hostList(hs []string) string {
	switch {
	case len(hs) <= 3:
		return "On " + strings.Join(hs, ", ")
	}
	return fmt.Sprintf("On %d of the targets (%s, …)", len(hs), strings.Join(hs[:3], ", "))
}
