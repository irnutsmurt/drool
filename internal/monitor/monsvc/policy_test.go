package monsvc

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"drool/internal/monitor"
	"drool/internal/monitor/storage"
)

func testPolicy(rows ...Override) *policy {
	p := emptyPolicy()
	p.groups["g-ai"] = &groupInfo{ID: "g-ai", Name: "AI Hosts"}
	p.groups["g-nas"] = &groupInfo{ID: "g-nas", Name: "NAS"}
	p.groups["g-lab"] = &groupInfo{ID: "g-lab", Name: "Lab"}
	p.members["h1"] = []string{"g-ai", "g-nas"}
	p.members["h2"] = []string{"g-nas"}
	for _, o := range rows {
		p.add(o)
	}
	return p
}

func val(t *testing.T, r Resolved) float64 {
	t.Helper()
	if r.Value == nil {
		t.Fatalf("no value: %+v", r)
	}
	return *r.Value
}

func TestPolicyPrecedence(t *testing.T) {
	sc := subjectCtx{keys: []string{"fsuuid:1111", "mount:/srv/media"}, label: "/srv/media"}
	t.Run("recommended default with no overrides", func(t *testing.T) {
		p := testPolicy()
		e := p.effective("h1", CheckFilesystem, sc, nil, levelAll)
		if val(t, e[FieldWarning]) != 85 || val(t, e[FieldCritical]) != 95 || e[FieldWarning].Source.Label != "Recommended default" {
			t.Fatalf("%+v", e)
		}
		if val(t, e[FieldClear]) != 83 || e[FieldClear].Source.Kind != "derived" {
			t.Fatalf("the clear level derives from the warning level: %+v", e[FieldClear])
		}
	})
	t.Run("global, group, host and subject in order; sparse fields inherit", func(t *testing.T) {
		p := testPolicy(
			Override{Scope: ScopeGlobal, Check: CheckFilesystem, Fields: Fields{FieldWarning: 80}},
			Override{Scope: ScopeGroup, GroupID: "g-nas", Check: CheckFilesystem, Fields: Fields{FieldWarning: 90, FieldCritical: 97}},
			Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Fields: Fields{FieldCritical: 98}},
			Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Subject: "fsuuid:1111", SubjectLabel: "/srv/media", Fields: Fields{FieldWarning: 92}},
		)
		e := p.effective("h1", CheckFilesystem, sc, nil, levelAll)
		if val(t, e[FieldWarning]) != 92 || e[FieldWarning].Source.Kind != "subject" || e[FieldWarning].Source.Label != "Custom for /srv/media" {
			t.Fatalf("subject beats host: %+v", e[FieldWarning])
		}
		if val(t, e[FieldCritical]) != 98 || e[FieldCritical].Source.Label != "Custom for this host" {
			t.Fatalf("host beats group: %+v", e[FieldCritical])
		}
		if val(t, e[FieldEnabled]) != 1 || e[FieldEnabled].Source.Kind != "recommended" {
			t.Fatalf("untouched fields inherit: %+v", e[FieldEnabled])
		}
		root := p.effective("h1", CheckFilesystem, subjectCtx{keys: []string{"mount:/"}, label: "/"}, nil, levelAll)
		if val(t, root[FieldWarning]) != 90 || root[FieldWarning].Source.Label != "Inherited from pack: NAS" {
			t.Fatalf("/ keeps the group's value: %+v", root[FieldWarning])
		}
		other := p.effective("h2", CheckFilesystem, sc, nil, levelAll)
		if val(t, other[FieldWarning]) != 90 || val(t, other[FieldCritical]) != 97 {
			t.Fatalf("the subject and host overrides belong to h1 only: %+v", other)
		}
		lab := testPolicy(Override{Scope: ScopeGlobal, Check: CheckFilesystem, Fields: Fields{FieldWarning: 80}})
		if g := lab.effective("h1", CheckFilesystem, sc, nil, levelAll)[FieldWarning]; val(t, g) != 80 || g.Source.Label != "Global custom setting" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("inherited is the value without the scope's own override", func(t *testing.T) {
		p := testPolicy(
			Override{Scope: ScopeGroup, GroupID: "g-nas", Check: CheckFilesystem, Fields: Fields{FieldWarning: 90}},
			Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Fields: Fields{FieldWarning: 70}},
		)
		inh := p.effective("h1", CheckFilesystem, subjectCtx{}, nil, levelHost)
		if val(t, inh[FieldWarning]) != 90 {
			t.Fatalf("reset to inherited restores the group's 90: %+v", inh[FieldWarning])
		}
	})
	t.Run("the stronger identity of a subject wins over its mount path", func(t *testing.T) {
		p := testPolicy(
			Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Subject: "mount:/srv/media", Fields: Fields{FieldWarning: 70}},
			Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Subject: "fsuuid:1111", Fields: Fields{FieldWarning: 92}},
		)
		if v := val(t, p.effective("h1", CheckFilesystem, sc, nil, levelAll)[FieldWarning]); v != 92 {
			t.Fatal(v)
		}
	})
}

func TestPolicyGroups(t *testing.T) {
	gpu := subjectCtx{keys: []string{"gpu:GPU-1"}, label: "RTX 4090"}
	t.Run("higher priority wins", func(t *testing.T) {
		p := testPolicy(
			Override{Scope: ScopeGroup, GroupID: "g-ai", Check: CheckCPU, Fields: Fields{FieldWarning: 99}},
			Override{Scope: ScopeGroup, GroupID: "g-nas", Check: CheckCPU, Fields: Fields{FieldWarning: 80}},
		)
		p.groups["g-ai"].Priority = 10
		r := p.effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)[FieldWarning]
		if val(t, r) != 99 || r.Conflict != nil || r.Source.Groups[0].Name != "AI Hosts" {
			t.Fatalf("%+v", r)
		}
		p.groups["g-ai"].Priority, p.groups["g-nas"].Priority = 0, 5 // priority changes
		if v := val(t, p.effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)[FieldWarning]); v != 80 {
			t.Fatal(v)
		}
	})
	t.Run("equal priority, different values: a visible conflict, never a silent choice", func(t *testing.T) {
		p := testPolicy(
			Override{Scope: ScopeGroup, GroupID: "g-ai", Check: CheckCPU, Fields: Fields{FieldWarning: 99}},
			Override{Scope: ScopeGroup, GroupID: "g-nas", Check: CheckCPU, Fields: Fields{FieldWarning: 80}},
			Override{Scope: ScopeGlobal, Check: CheckCPU, Fields: Fields{FieldDuration: 7}},
		)
		r := p.effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)[FieldWarning]
		if r.Conflict == nil || len(r.Conflict.Groups) != 2 || val(t, r) != 95 || !strings.Contains(r.Source.Label, "Conflict") {
			t.Fatalf("the conflicted field uses the next scope (Recommended) and says so: %+v", r)
		}
		if p.counts("h1").Conflicts != 1 {
			t.Fatal("the conflict is counted")
		}
		// The evaluator gives the same answer whatever the membership order.
		for i := 0; i < 50; i++ {
			rand.Shuffle(len(p.members["h1"]), func(a, b int) { p.members["h1"][a], p.members["h1"][b] = p.members["h1"][b], p.members["h1"][a] })
			r2 := p.effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)[FieldWarning]
			if val(t, r2) != 95 || r2.Conflict.Groups[0].Name != "AI Hosts" {
				t.Fatalf("order-dependent: %+v", r2)
			}
		}
		// A host override settles it.
		p.add(Override{Scope: ScopeHost, HostID: "h1", Check: CheckCPU, Fields: Fields{FieldWarning: 97}})
		r = p.effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)[FieldWarning]
		if val(t, r) != 97 || r.Conflict != nil {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("equal priority, same value: agreement, not a conflict", func(t *testing.T) {
		p := testPolicy(
			Override{Scope: ScopeGroup, GroupID: "g-ai", Check: CheckGPUUtil, Fields: Fields{FieldEnabled: 1}},
			Override{Scope: ScopeGroup, GroupID: "g-nas", Check: CheckGPUUtil, Fields: Fields{FieldEnabled: 1}},
		)
		r := p.effective("h1", CheckGPUUtil, gpu, nil, levelAll)[FieldEnabled]
		if val(t, r) != 1 || r.Conflict != nil || len(r.Source.Groups) != 2 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("host changes groups", func(t *testing.T) {
		p := testPolicy(Override{Scope: ScopeGroup, GroupID: "g-lab", Check: CheckCPU, Fields: Fields{FieldEnabled: 0}})
		if !p.rule("h2", CheckCPU, subjectCtx{}, nil).on {
			t.Fatal("not in Lab yet")
		}
		before := p.sig("h2", CheckCPU)
		p.members["h2"] = append(p.members["h2"], "g-lab")
		if p.rule("h2", CheckCPU, subjectCtx{}, nil).on || p.sig("h2", CheckCPU) == before {
			t.Fatal("joining Lab turns CPU off and changes the host's signature")
		}
	})
}

func TestPolicyChecks(t *testing.T) {
	gpu := subjectCtx{keys: []string{"gpu:GPU-1"}, label: "RTX 4090"}
	t.Run("GPU utilization is off in Recommended; enabling it doesn't touch temperature", func(t *testing.T) {
		p := testPolicy()
		if p.rule("h1", CheckGPUUtil, gpu, nil).on {
			t.Fatal("GPU utilization must be off by default")
		}
		p.add(Override{Scope: ScopeGroup, GroupID: "g-ai", Check: CheckGPUUtil, Fields: Fields{FieldEnabled: 1}})
		r := p.rule("h1", CheckGPUUtil, gpu, nil)
		if !r.on || *r.warn != 95 || r.dur.Minutes() != 30 || *r.clr != 85 {
			t.Fatalf("%+v", r)
		}
		p.add(Override{Scope: ScopeHost, HostID: "h1", Check: CheckGPUUtil, Subject: "gpu:GPU-1", Fields: Fields{FieldEnabled: 0}})
		if p.rule("h1", CheckGPUUtil, gpu, nil).on {
			t.Fatal("the subject override turns it off for this GPU")
		}
		if !p.rule("h1", CheckTemperature, gpu, &hwLimits{crit: fp(90)}).on {
			t.Fatal("GPU temperature stays on")
		}
	})
	t.Run("temperature: hardware limits unless an explicit override", func(t *testing.T) {
		p := testPolicy()
		hw := &hwLimits{high: fp(80), crit: fp(100)}
		sens := subjectCtx{keys: []string{"sensor:coretemp@coretemp.0/Package id 0"}, label: "CPU"}
		e := p.effective("h1", CheckTemperature, sens, hw, levelAll)
		if val(t, e[FieldWarning]) != 80 || e[FieldWarning].Source.Kind != "hardware" || e[FieldWarning].Source.Label != "Hardware-reported limit" || val(t, e[FieldClear]) != 77 {
			t.Fatalf("%+v", e)
		}
		if r := p.effective("h1", CheckTemperature, sens, &hwLimits{}, levelAll)[FieldWarning]; r.Value != nil || r.Source.Label != "No hardware limit reported" {
			t.Fatalf("no hardware limit and no override: no threshold is invented: %+v", r)
		}
		if r := p.effective("h1", CheckTemperature, subjectCtx{}, nil, levelAll)[FieldWarning]; r.Value != nil || r.Source.Kind != "hardware" || r.Source.Label != "Each sensor's hardware limit" {
			t.Fatalf("at the fleet or host level, each sensor's own limit: %+v", r)
		}
		p.add(Override{Scope: ScopeHost, HostID: "h1", Check: CheckTemperature, Subject: sens.keys[0], Fields: Fields{FieldWarning: 70, FieldDuration: 10}})
		r := p.rule("h1", CheckTemperature, sens, hw)
		if *r.warn != 70 || r.warnKind != "subject" || *r.crit != 100 || r.dur.Minutes() != 10 {
			t.Fatalf("explicit warning, hardware critical, custom duration: %+v", r)
		}
	})
	t.Run("filesystem threshold never weakens physical disk health", func(t *testing.T) {
		p := testPolicy(Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Fields: Fields{FieldWarning: 99, FieldCritical: 100}})
		for _, c := range []string{CheckStorageHealth, CheckMediaErrors, CheckKernelStorage, CheckDeviceReadOnly} {
			if !p.rule("h1", c, subjectCtx{keys: []string{"disk:wwn-0x5000"}}, nil).on {
				t.Fatal(c)
			}
		}
	})
	t.Run("critical never below warning when scopes mix", func(t *testing.T) {
		p := testPolicy(Override{Scope: ScopeGlobal, Check: CheckMemory, Fields: Fields{FieldCritical: 92}},
			Override{Scope: ScopeHost, HostID: "h1", Check: CheckMemory, Fields: Fields{FieldWarning: 95}})
		r := p.rule("h1", CheckMemory, subjectCtx{}, nil)
		if *r.crit != 95 {
			t.Fatalf("%+v", r)
		}
	})
}

func TestFieldsJSONAndValidation(t *testing.T) {
	var f Fields
	if err := json.Unmarshal([]byte(`{"enabled":false,"warning":92,"critical":null}`), &f); err != nil {
		t.Fatal(err)
	}
	if len(f) != 2 || f[FieldEnabled] != 0 || f[FieldWarning] != 92 {
		t.Fatalf("null is inherited, not stored: %v", f)
	}
	b, _ := json.Marshal(f)
	if string(b) != `{"enabled":false,"warning":92}` {
		t.Fatal(string(b))
	}
	for _, c := range []struct {
		check string
		f     Fields
		ok    bool
	}{
		{CheckFilesystem, Fields{FieldWarning: 92, FieldCritical: 98}, true},
		{CheckFilesystem, Fields{FieldWarning: 98, FieldCritical: 92}, false},
		{CheckFilesystem, Fields{FieldDuration: 5}, false}, // filesystems have no duration
		{CheckStorageHealth, Fields{FieldWarning: 50}, false},
		{CheckStorageHealth, Fields{FieldEnabled: 0}, true},
		{CheckCPU, Fields{FieldWarning: 101}, false},
		{CheckCPU, Fields{FieldDuration: 2.5}, false},
		{CheckCPU, Fields{FieldClear: 96, FieldWarning: 95}, false},
		{CheckStale, Fields{FieldIntervals: 1}, false},
		{CheckTemperature, Fields{FieldWarning: 75}, true},
		{"nope", Fields{}, false},
	} {
		if err := c.f.Validate(c.check); (err == nil) != c.ok {
			t.Errorf("%s %v: %v", c.check, c.f, err)
		}
	}
}

func TestSubjectIdentity(t *testing.T) {
	rep := &storage.Report{Devices: []storage.Device{
		{Key: "wwn-0x5000c500a1b2c3d4", Name: "sda", Partitions: []storage.Partition{{Name: "sda1", UUID: "aaaa", Mounts: []string{"/srv/media"}}}},
		{Key: "nvme-eui.0025388b91b2", Name: "nvme0n1", Partitions: []storage.Partition{
			{Name: "nvme0n1p2", UUID: "bbbb", Mounts: []string{"/", "/home"}}, // btrfs subvolumes: shared UUID
			{Name: "nvme0n1p3", UUID: "cccc", Mounts: []string{"/var"}, Via: "vg-var"}}},
		{Key: "name:sdz", Name: "sdz"},
	}}
	u := mountUUIDs(rep)
	if u["/srv/media"] != "aaaa" || u["/"] != "" || u["/home"] != "" || u["/var"] != "" {
		t.Fatalf("UUID only when it names one filesystem directly: %v", u)
	}
	if s := fsSubject("/srv/media", u); s.Key != "fsuuid:aaaa" || s.Strength != IdentityStable {
		t.Fatal(s)
	}
	if s := fsSubject("/", u); s.Key != "mount:/" || s.Strength != IdentityLocation {
		t.Fatal(s)
	}
	if s := diskSubject(rep.Devices[0]); s.Key != "disk:wwn-0x5000c500a1b2c3d4" || s.Strength != IdentityStable {
		t.Fatal(s)
	}
	if s := diskSubject(rep.Devices[2]); s.Strength != IdentityWeak {
		t.Fatal("a kernel name is weak and says so")
	}
	nv := monitor.GPU{Index: 1, UUID: "GPU-8f3b", PCI: "0000:01:00.0", Model: "RTX 4090"}
	if s := gpuSubject(nv); s.Key != "gpu:GPU-8f3b" || s.Strength != IdentityStable {
		t.Fatal(s)
	}
	nv.Index = 0 // reordered after a reboot: same identity
	if s := gpuSubject(nv); s.Key != "gpu:GPU-8f3b" {
		t.Fatal(s)
	}
	if s := gpuSubject(monitor.GPU{Index: 0, PCI: "0000:03:00.0"}); s.Key != "gpu:pci:0000:03:00.0" || s.Strength != IdentityLocation {
		t.Fatal(s)
	}
	if s := gpuSubject(monitor.GPU{Index: 2}); s.Key != "gpu:index:2" || s.Strength != IdentityWeak {
		t.Fatal(s)
	}
	disks := diskKeysByName(rep)
	if s := sensorSubject(monitor.Sensor{ID: "drivetemp:sda/temp1", Chip: "drivetemp", Label: "temp1", Kind: "storage", Device: "sda", HW: "0:0:0:0"}, disks); s.Key != "sensor:disk:wwn-0x5000c500a1b2c3d4/temp1" || s.Strength != IdentityStable {
		t.Fatal(s)
	}
	if s := sensorSubject(monitor.Sensor{ID: "k10temp/Tctl", Chip: "k10temp", Label: "Tctl", Kind: "cpu", HW: "0000:00:18.3"}, disks); s.Key != "sensor:k10temp@0000:00:18.3/Tctl" || s.Strength != IdentityLocation {
		t.Fatal(s)
	}
	if s := sensorSubject(monitor.Sensor{ID: "acpitz.1/temp1", Chip: "acpitz", Label: "temp1"}, disks); s.Strength != IdentityWeak {
		t.Fatal(s)
	}
	for _, c := range []struct {
		check, key string
		ok         bool
	}{{CheckFilesystem, "fsuuid:aaaa", true}, {CheckFilesystem, "mount:/srv", true}, {CheckFilesystem, "mount:", false},
		{CheckServiceFailed, "unit:nginx.service", true}, {CheckServiceFailed, "gpu:x", false}, {CheckCPU, "x", false},
		{CheckTemperature, "gpu:GPU-1", true}, {CheckGPUUtil, "gpu:GPU-1", true}} {
		if validSubjectKey(c.check, c.key) != c.ok {
			t.Errorf("%s %s", c.check, c.key)
		}
	}
}

func TestWeaker(t *testing.T) {
	p := testPolicy()
	hw := &hwLimits{high: fp(80), crit: fp(100)}
	base := p.effective("h1", CheckFilesystem, subjectCtx{}, hw, levelAll)
	for _, c := range []struct {
		f    Fields
		weak bool
	}{
		{Fields{FieldEnabled: 0}, true},
		{Fields{FieldWarning: 90}, true},
		{Fields{FieldWarning: 70}, false},
		{Fields{FieldCritical: 99}, true},
		{Fields{FieldClear: 84}, true}, // clears sooner
		{Fields{FieldClear: 60}, false},
	} {
		np := p.with(OverrideKey{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem}, c.f)
		if weaker(base, np.effective("h1", CheckFilesystem, subjectCtx{}, hw, levelAll)) != c.weak {
			t.Errorf("%v", c.f)
		}
	}
	cpu := p.effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)
	if !weaker(cpu, p.with(OverrideKey{Scope: ScopeHost, HostID: "h1", Check: CheckCPU}, Fields{FieldDuration: 10}).effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)) {
		t.Error("a longer duration is weaker")
	}
	if weaker(cpu, p.with(OverrideKey{Scope: ScopeHost, HostID: "h1", Check: CheckCPU}, Fields{FieldDuration: 2}).effective("h1", CheckCPU, subjectCtx{}, nil, levelAll)) {
		t.Error("a shorter duration is stricter")
	}
}

// BenchmarkRule: what one evaluation pays for the configuration (a host in
// three groups, overrides at every scope).
func BenchmarkRule(b *testing.B) {
	p := testPolicy(
		Override{Scope: ScopeGlobal, Check: CheckFilesystem, Fields: Fields{FieldWarning: 80}},
		Override{Scope: ScopeGroup, GroupID: "g-nas", Check: CheckFilesystem, Fields: Fields{FieldWarning: 90, FieldCritical: 97}},
		Override{Scope: ScopeGroup, GroupID: "g-ai", Check: CheckFilesystem, Fields: Fields{FieldWarning: 91}},
		Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Fields: Fields{FieldCritical: 98}},
		Override{Scope: ScopeHost, HostID: "h1", Check: CheckFilesystem, Subject: "fsuuid:1", Fields: Fields{FieldWarning: 92}},
	)
	p.members["h1"] = append(p.members["h1"], "g-lab")
	sc := subjectCtx{keys: []string{"fsuuid:1", "mount:/srv"}, label: "/srv"}
	b.ReportAllocs()
	for b.Loop() {
		p.rule("h1", CheckFilesystem, sc, nil)
	}
}
