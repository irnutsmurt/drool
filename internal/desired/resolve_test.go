package desired

import (
	"math/rand"
	"strings"
	"testing"
)

func bl(id, name string, auto bool, rs ...Resource) *Baseline {
	b := &Baseline{ID: id, Name: name, Revision: 1, Resources: map[string]Resource{}, Auto: auto}
	for _, r := range rs {
		if err := r.Normalize(); err != nil {
			panic(err)
		}
		b.Resources[r.Key()] = r
	}
	return b
}

func snapFor() *Snapshot {
	return &Snapshot{Baselines: map[string]*Baseline{}, ByGroup: map[string][]string{}, ByHost: map[string][]string{},
		Overrides: map[string]map[string]Override{}, Groups: map[string]Group{}, Members: map[string][]string{},
		Hosts: map[string]string{"h1": "lab-box", "h2": "web-1"}}
}

func TestResolvePrecedence(t *testing.T) {
	const nginx = "service:nginx.service"
	s := snapFor()
	s.Baselines["g"] = bl("g", "Server Defaults", false, svc("nginx", StateRunning, on()), pkgR("curl", StateInstalled), pkgR("telnet", StateAbsent))
	s.Global = []string{"g"}

	t.Run("global", func(t *testing.T) {
		hs := s.Resolve("h1")
		e := hs.Resources[nginx]
		if e.Conflict || e.Resource.State != StateRunning || e.Source.Level != LevelGlobal {
			t.Fatalf("got %+v", e)
		}
		if e.Source.Label != "Required by Global baseline Server Defaults" {
			t.Fatalf("label %q", e.Source.Label)
		}
		if len(hs.Resources) != 3 {
			t.Fatalf("%d resources", len(hs.Resources))
		}
	})

	t.Run("group beats global", func(t *testing.T) {
		s := cloneSnap(s)
		s.Baselines["m"] = bl("m", "Maintenance", false, svc("nginx", StateStopped, nil))
		s.Groups["ga"] = Group{Name: "Maintenance Lab"}
		s.ByGroup["ga"] = []string{"m"}
		s.Members["h1"] = []string{"ga"}
		e := s.Resolve("h1").Resources[nginx]
		if e.Resource.State != StateStopped || e.Resource.Enabled != nil || e.Source.Level != LevelGroup {
			t.Fatalf("got %+v", e)
		}
		if e.Source.Label != "Inherited from pack: Maintenance Lab (baseline Maintenance)" {
			t.Fatalf("label %q", e.Source.Label)
		}
		if len(e.Source.Shadowed) != 1 || e.Source.Shadowed[0].Level != LevelGlobal {
			t.Fatalf("shadowed %+v", e.Source.Shadowed)
		}
		// The other host still follows the global baseline.
		if e2 := s.Resolve("h2").Resources[nginx]; e2.Resource.State != StateRunning {
			t.Fatalf("h2 %+v", e2)
		}
	})

	t.Run("equal priority conflict, then priority, then host", func(t *testing.T) {
		s := cloneSnap(s)
		s.Baselines["w"] = bl("w", "Web", false, svc("nginx", StateRunning, nil))
		s.Baselines["m"] = bl("m", "Maintenance", false, svc("nginx", StateStopped, nil))
		s.Groups["gw"] = Group{Name: "Web Servers"}
		s.Groups["gm"] = Group{Name: "Maintenance Lab"}
		s.ByGroup["gw"], s.ByGroup["gm"] = []string{"w"}, []string{"m"}
		s.Members["h1"] = []string{"gw", "gm"}
		e := s.Resolve("h1").Resources[nginx]
		if !e.Conflict || e.Resource != nil {
			t.Fatalf("want a conflict, got %+v", e)
		}
		if e.Source.Label != "Conflict between packs Maintenance Lab and Web Servers (same priority 0)" {
			t.Fatalf("label %q", e.Source.Label)
		}
		if len(e.Source.Competing) != 2 {
			t.Fatalf("competing %+v", e.Source.Competing)
		}
		wants := e.Source.Competing[0].Want + "|" + e.Source.Competing[1].Want
		if !strings.Contains(wants, "running") || !strings.Contains(wants, "stopped") {
			t.Fatalf("competing wants %q", wants)
		}
		// Order never decides: shuffled memberships give the same result.
		for i := 0; i < 20; i++ {
			s2 := cloneSnap(s)
			m := append([]string(nil), s.Members["h1"]...)
			rand.Shuffle(len(m), func(a, b int) { m[a], m[b] = m[b], m[a] })
			s2.Members["h1"] = m
			if h := s2.Resolve("h1").Hash; h != s.Resolve("h1").Hash {
				t.Fatal("membership order changed the result")
			}
		}
		// A priority settles it.
		s3 := cloneSnap(s)
		s3.Groups["gw"] = Group{Name: "Web Servers", Priority: 10}
		e = s3.Resolve("h1").Resources[nginx]
		if e.Conflict || e.Resource.State != StateRunning || len(e.Source.Groups) != 1 || e.Source.Groups[0].Priority != 10 {
			t.Fatalf("priority: %+v", e)
		}
		// A host-specific declaration settles it too.
		s4 := cloneSnap(s)
		s4.Overrides["h1"] = map[string]Override{nginx: {Resource: svc("nginx.service", StateStopped, nil), Info: OverrideInfo{Origin: "manual"}}}
		e = s4.Resolve("h1").Resources[nginx]
		if e.Conflict || e.Resource.State != StateStopped || e.Source.Level != LevelHost || e.Source.Label != "Custom for this host" {
			t.Fatalf("host: %+v", e)
		}
	})

	t.Run("agreeing groups", func(t *testing.T) {
		s := cloneSnap(s)
		s.Baselines["a"] = bl("a", "A", false, pkgR("htop", StateInstalled))
		s.Baselines["b"] = bl("b", "B", false, pkgR("htop", StateInstalled))
		s.Groups["ga"], s.Groups["gb"] = Group{Name: "Alpha"}, Group{Name: "Beta"}
		s.ByGroup["ga"], s.ByGroup["gb"] = []string{"a"}, []string{"b"}
		s.Members["h1"] = []string{"gb", "ga"}
		e := s.Resolve("h1").Resources["package:htop"]
		if e.Conflict || e.Source.Label != "Inherited from packs: Alpha, Beta (they agree)" || len(e.Source.Baselines) != 2 {
			t.Fatalf("got %+v", e)
		}
	})

	t.Run("assigned to the host beats groups", func(t *testing.T) {
		s := cloneSnap(s)
		s.Baselines["m"] = bl("m", "Maintenance", false, svc("nginx", StateStopped, nil))
		s.Baselines["x"] = bl("x", "Lab", false, svc("nginx", StateRunning, off()))
		s.Groups["gm"] = Group{Name: "Maintenance Lab", Priority: 100}
		s.ByGroup["gm"] = []string{"m"}
		s.Members["h1"] = []string{"gm"}
		s.ByHost["h1"] = []string{"x"}
		e := s.Resolve("h1").Resources[nginx]
		if e.Source.Level != LevelAssigned || e.Resource.State != StateRunning || *e.Resource.Enabled {
			t.Fatalf("got %+v", e)
		}
	})

	t.Run("two global baselines disagree", func(t *testing.T) {
		s := cloneSnap(s)
		s.Baselines["g2"] = bl("g2", "Hardening", false, pkgR("curl", StateAbsent))
		s.Global = []string{"g", "g2"}
		e := s.Resolve("h1").Resources["package:curl"]
		if !e.Conflict || e.Source.Label != "Conflict between Global baselines Hardening and Server Defaults" {
			t.Fatalf("got %+v", e)
		}
	})

	t.Run("exclusion", func(t *testing.T) {
		s := cloneSnap(s)
		s.Overrides["h1"] = map[string]Override{"package:telnet": {Info: OverrideInfo{Excluded: true, Origin: "accept"}}}
		hs := s.Resolve("h1")
		if _, ok := hs.Resources["package:telnet"]; ok {
			t.Fatal("an excluded resource is still managed")
		}
		if src, ok := hs.Excluded["package:telnet"]; !ok || src.Label != "Not managed on this host" {
			t.Fatalf("excluded %+v", hs.Excluded)
		}
	})

	t.Run("automatic restore follows the deciding baseline", func(t *testing.T) {
		s := cloneSnap(s)
		s.Baselines["g"] = bl("g", "Server Defaults", true, svc("nginx", StateRunning, on()))
		e := s.Resolve("h1").Resources[nginx]
		if !e.Source.Auto || e.Source.AutoBaseline == nil || e.Source.AutoBaseline.ID != "g" {
			t.Fatalf("got %+v", e.Source)
		}
		s.Overrides["h1"] = map[string]Override{nginx: {Resource: svc("nginx.service", StateStopped, nil)}}
		if e := s.Resolve("h1").Resources[nginx]; e.Source.Auto {
			t.Fatal("a host-specific declaration is never restored automatically")
		}
	})
}

func cloneSnap(s *Snapshot) *Snapshot {
	c := s.clone()
	c.Members = map[string][]string{}
	for k, v := range s.Members {
		c.Members[k] = append([]string(nil), v...)
	}
	c.Overrides = map[string]map[string]Override{}
	for k, v := range s.Overrides {
		c.Overrides[k] = v
	}
	return c
}

func TestResourceValidation(t *testing.T) {
	bad := []Resource{
		{Kind: KindService, Name: "nginx", State: "restarted"},
		{Kind: KindService, Name: "nginx"},
		{Kind: KindService, Name: "certbot.timer", State: StateRunning},
		{Kind: KindPackage, Name: "curl", State: "latest"},
		{Kind: KindPackage, Name: "bad name", State: StateInstalled},
		{Kind: KindFile, Path: "relative", Artifact: strings.Repeat("a", 64)},
		{Kind: KindFile, Path: "/etc/x", Artifact: "nothex"},
		{Kind: KindFile, Path: "/etc/x", Artifact: strings.Repeat("a", 64), Mode: "999"},
		{Kind: "script"},
	}
	for _, r := range bad {
		if err := r.Normalize(); err == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
	r := Resource{Kind: KindService, Name: " jellyfin ", State: StateRunning, Enabled: on()}
	if err := r.Normalize(); err != nil || r.Name != "jellyfin.service" || r.Key() != "service:jellyfin.service" {
		t.Fatalf("%+v %v", r, err)
	}
	f := Resource{Kind: KindFile, Path: "/etc//caddy/./Caddyfile", Artifact: strings.Repeat("a", 64), Mode: "644", ArtifactName: "Caddyfile"}
	if err := f.Normalize(); err != nil || f.Path != "/etc/caddy/Caddyfile" || f.Mode != "0644" {
		t.Fatalf("%+v %v", f, err)
	}
	g := f
	g.ArtifactName = "other name"
	if !f.Same(g) {
		t.Fatal("the display name changes the declaration")
	}
	if _, err := NormalizeResources([]Resource{pkgR("curl", StateInstalled), pkgR("curl", StateAbsent)}); err == nil {
		t.Fatal("a key declared twice was accepted")
	}
}

func TestCompare(t *testing.T) {
	en := func(b bool) *bool { return &b }
	cases := []struct {
		name  string
		r     Resource
		o     Observation
		diffs string
		block bool
	}{
		{"running enabled", svc("a.service", StateRunning, en(true)), Observation{Service: &ServiceObs{Found: true, Load: "loaded", Active: "active", Sub: "running", FileState: "enabled"}}, "", false},
		{"stopped", svc("a.service", StateRunning, en(true)), Observation{Service: &ServiceObs{Found: true, Load: "loaded", Active: "inactive", Sub: "dead", FileState: "enabled"}}, "state: running → stopped", false},
		{"failed and disabled", svc("a.service", StateRunning, en(true)), Observation{Service: &ServiceObs{Found: true, Load: "loaded", Active: "failed", Sub: "failed", FileState: "disabled"}}, "state: running → failed; enabled: enabled at boot → disabled at boot", false},
		{"should be stopped", svc("a.service", StateStopped, en(false)), Observation{Service: &ServiceObs{Found: true, Load: "loaded", Active: "active", Sub: "running", FileState: "enabled"}}, "state: stopped → running; enabled: disabled at boot → enabled at boot", false},
		{"missing unit, stopped wanted", svc("a.service", StateStopped, en(false)), Observation{Service: &ServiceObs{Found: false}}, "", false},
		{"missing unit, running wanted", svc("a.service", StateRunning, nil), Observation{Service: &ServiceObs{Found: false}}, "state: running → not installed", false},
		{"masked", svc("a.service", StateRunning, nil), Observation{Service: &ServiceObs{Found: true, Load: "masked", Active: "inactive", FileState: "masked"}}, "state: running → stopped", true},
		{"static can't be enabled", svc("a.service", "", en(true)), Observation{Service: &ServiceObs{Found: true, Load: "loaded", Active: "inactive", FileState: "static"}}, "enabled: enabled at boot → static", true},
		{"installed", pkgR("curl", StateInstalled), Observation{Package: &PackageObs{Installed: true, Version: "8.0"}}, "", false},
		{"missing package", pkgR("curl", StateInstalled), Observation{Package: &PackageObs{}}, "installed: installed → not installed", false},
		{"prohibited present", pkgR("telnet", StateAbsent), Observation{Package: &PackageObs{Installed: true, Version: "0.17"}}, "installed: absent → installed (0.17)", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.r
			if err := r.Normalize(); err != nil {
				t.Fatal(err)
			}
			j := Compare(r, c.o)
			if got := DiffText(j.Diffs); got != c.diffs {
				t.Fatalf("diffs %q, want %q", got, c.diffs)
			}
			if (j.Blocker != "") != c.block {
				t.Fatalf("blocker %q", j.Blocker)
			}
		})
	}
	sha := strings.Repeat("b", 64)
	f := Resource{Kind: KindFile, Path: "/etc/x", Artifact: sha, Mode: "0644", Owner: "root"}
	if err := f.Normalize(); err != nil {
		t.Fatal(err)
	}
	if j := Compare(f, Observation{File: &FileObs{Exists: false}}); DiffText(j.Diffs) != "exists: present → missing" {
		t.Fatal(DiffText(j.Diffs))
	}
	if j := Compare(f, Observation{File: &FileObs{Exists: true, Type: "symlink", Target: "/tmp/y"}}); j.Blocker == "" || DiffText(j.Diffs) != "type: regular file → symlink to /tmp/y" {
		t.Fatal(DiffText(j.Diffs), j.Blocker)
	}
	j := Compare(f, Observation{File: &FileObs{Exists: true, Type: "file", SHA256: strings.Repeat("c", 64), Mode: "0664", Owner: "www", Group: "www"}})
	if got := DiffText(j.Diffs); got != "content: sha256 bbbbbbbbbbbb… → sha256 cccccccccccc…; owner: root → www; mode: 0644 → 0664" {
		t.Fatal(got)
	}
	// An owner given by number matches that uid.
	f.Owner = "0"
	if j := Compare(f, Observation{File: &FileObs{Exists: true, Type: "file", SHA256: sha, Mode: "0644", Owner: "root", UID: 0}}); !j.Compliant() {
		t.Fatal(DiffText(j.Diffs))
	}
}
