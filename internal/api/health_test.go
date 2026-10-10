package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"drool/internal/alerts"
	"drool/internal/auth"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/monsvc"
	"drool/internal/monitor/services"
	"drool/internal/monitor/storage"
)

// M12.1: health overrides. These tests drive M11's real evaluator (points,
// states, storage and service reports through the ingestion path) and M12's
// real alerting, with a local webhook receiver.

func (e *alertEnv) put(c *client, body map[string]any) resp {
	e.t.Helper()
	return c.do("PUT", "/api/v1/monitoring/health/overrides", body)
}

func (e *alertEnv) mustPut(body map[string]any) {
	e.t.Helper()
	if r := e.put(e.c, body); r.status != 200 {
		e.t.Fatalf("override %v: %d %s", body, r.status, r.body)
	}
}

func (e *alertEnv) points(h string, minutes int, v map[string]float64) {
	e.t.Helper()
	for range minutes {
		send(e.t, e.h.mon, h, agentmon.KindMetrics, monitor.Batch{Points: []monitor.Point{pointAt(e.clk.now(), v)}})
		e.clk.add(time.Minute)
	}
}

// states sends a state each minute for n minutes.
func (e *alertEnv) states(h string, n int, st func() monitor.State) {
	e.t.Helper()
	for range n {
		s := st()
		s.At = e.clk.now().UnixMilli()
		send(e.t, e.h.mon, h, agentmon.KindState, s)
		e.clk.add(time.Minute)
	}
}

func (e *alertEnv) issuesOf(h, state string) map[string]monsvc.Issue {
	e.t.Helper()
	var out struct{ Issues []monsvc.Issue }
	e.c.do("GET", "/api/v1/monitoring/issues?state="+state+"&host_id="+h, nil).json(e.t, &out)
	m := map[string]monsvc.Issue{}
	for _, i := range out.Issues {
		if _, seen := m[i.Check+"|"+i.Subject]; !seen { // newest first
			m[i.Check+"|"+i.Subject] = i
		}
	}
	return m
}

func (e *alertEnv) hostView(h, query string) monsvc.HealthView {
	e.t.Helper()
	var v monsvc.HealthView
	r := e.c.do("GET", "/api/v1/hosts/"+h+"/monitoring/health"+query, nil)
	if r.status != 200 {
		e.t.Fatalf("host health: %d %s", r.status, r.body)
	}
	r.json(e.t, &v)
	return v
}

func field(t *testing.T, v monsvc.HealthView, check, name string) monsvc.EffectiveField {
	t.Helper()
	for _, c := range v.Checks {
		if c.Check == check {
			for _, f := range c.Fields {
				if f.Name == name {
					return f
				}
			}
		}
	}
	t.Fatalf("no %s.%s in %+v", check, name, v.Checks)
	return monsvc.EffectiveField{}
}

func num(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}

var cpuLoad = func(p float64) map[string]float64 { return map[string]float64{"cpu": p, "mem": 20} }

func TestHealthOverridesCPUAndConfigurationClears(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	e.dest("hook", "webhook", rcv.srv.URL+"/hook", "recommended")
	render, web := e.host("render01"), e.host("web01")

	// Recommended: CPU at 99% for 6 minutes is an issue on both.
	for _, h := range []string{render, web} {
		e.points(h, 6, cpuLoad(99))
	}
	if len(e.list("view=active")) != 2 {
		t.Fatal("CPU alerts")
	}
	e.deliver()
	cpuAlert := func(h string) alerts.Alert {
		for _, a := range e.list("view=all&host_id=" + h) {
			if a.Check == "cpu" {
				return a
			}
		}
		t.Fatalf("no cpu alert on %s", h)
		return alerts.Alert{}
	}
	first := cpuAlert(render)

	// Disable CPU for the compute host only: its issue closes as a
	// configuration change, the other host keeps alerting.
	e.mustPut(map[string]any{"scope": "host", "host_id": render, "check": "cpu", "fields": map[string]any{"enabled": false}})
	is := e.issuesOf(render, "resolved")["cpu|"]
	if is.Resolution != "disabled" || !strings.Contains(is.ResolutionNote, "turned off") || !strings.Contains(is.ResolutionNote, "Custom for this host") {
		t.Fatalf("issue resolution: %+v", is)
	}
	a := e.detail(first.ID).Alert
	if a.State != "resolved" || a.Resolution != "disabled" {
		t.Fatalf("alert: %+v", a)
	}
	if k := timelineKinds(e.detail(first.ID)); !slices.Contains(k, "cleared") || slices.Contains(k, "recovered") {
		t.Fatalf("the timeline says cleared, never recovered: %v", k)
	}
	if e.deliver() != 1 {
		t.Fatal("closing notification")
	}
	last := rcv.last()
	if last.Event != "cleared" || last.Alert.Resolution != "disabled" || !strings.HasPrefix(last.Summary, "CLEARED BY CONFIGURATION") ||
		!strings.Contains(last.Text, "This is not a recovery") || strings.Contains(strings.ToLower(last.Summary), "recovered") {
		t.Fatalf("closing payload: %s", rcv.raw[len(rcv.raw)-1])
	}
	if slices.Contains(rcv.events(), "recovered") {
		t.Fatalf("a configuration change was reported as a recovery: %v", rcv.events())
	}
	var deliveries struct{ Items []alerts.Delivery }
	e.c.do("GET", "/api/v1/notifications/deliveries?alert_id="+first.ID, nil).json(t, &deliveries)
	if len(deliveries.Items) != 2 || deliveries.Items[0].Kind != "cleared" {
		t.Fatalf("the closing delivery shows as cleared: %+v", deliveries.Items)
	}
	e.points(render, 10, cpuLoad(99))
	e.points(web, 10, cpuLoad(99))
	if e.issuesOf(render, "active")["cpu|"].ID != "" {
		t.Fatal("CPU disabled for render01 but still evaluated")
	}
	if cpuAlert(web).State == "resolved" {
		t.Fatal("CPU still enabled elsewhere")
	}
	// Memory pressure on the compute host is independent of the CPU check.
	e.points(render, 6, map[string]float64{"cpu": 99, "mem": 95})
	if e.issuesOf(render, "active")["memory|"].ID == "" {
		t.Fatal("memory pressure must still be judged with CPU off")
	}

	// Re-enabled: the duration starts at the change, not 10 minutes back.
	var ov struct{ Items []monsvc.Override }
	e.c.do("GET", "/api/v1/monitoring/health/overrides?host_id="+render, nil).json(t, &ov)
	if len(ov.Items) != 1 {
		t.Fatalf("overrides: %+v", ov.Items)
	}
	if r := e.c.do("DELETE", "/api/v1/monitoring/health/overrides/"+ov.Items[0].ID, nil); r.status != 204 {
		t.Fatalf("reset: %d %s", r.status, r.body)
	}
	e.points(render, 4, cpuLoad(99))
	if e.issuesOf(render, "active")["cpu|"].ID != "" {
		t.Fatal("re-enabled CPU fired before its duration, counted from the change")
	}
	e.points(render, 1, cpuLoad(99))
	second := e.issuesOf(render, "active")["cpu|"]
	if second.ID == "" || second.ID == is.ID {
		t.Fatalf("a new issue after the duration: %+v", second)
	}
	if b := cpuAlert(render); b.ID == first.ID || b.State != "active" {
		t.Fatalf("a new occurrence: %+v", b)
	}
}

func TestHealthOverridesThresholdChanges(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	e.dest("hook", "webhook", rcv.srv.URL+"/hook", "recommended")
	h := e.host("db01")

	t.Run("still failing: the same issue and alert, severity follows the rule", func(t *testing.T) {
		e.points(h, 6, map[string]float64{"cpu": 10, "mem": 93})
		is := e.issuesOf(h, "active")["memory|"]
		if is.Severity != "warning" {
			t.Fatalf("%+v", is)
		}
		al := e.only("view=active&host_id=" + h)
		e.deliver()
		e.mustPut(map[string]any{"scope": "global", "check": "memory", "fields": map[string]any{"critical": 92}})
		now := e.issuesOf(h, "active")["memory|"]
		if now.ID != is.ID || now.Severity != "critical" {
			t.Fatalf("same issue, critical now: %+v", now)
		}
		if b := e.only("view=active&host_id=" + h); b.ID != al.ID || b.Severity != "critical" {
			t.Fatalf("same alert, escalated: %+v", b)
		}
		e.deliver()
		if ev := rcv.events(); ev[len(ev)-1] != "escalated" {
			t.Fatalf("%v", ev)
		}
		// Threshold raised above the value: closes as reconfigured.
		e.mustPut(map[string]any{"scope": "global", "check": "memory", "fields": map[string]any{"warning": 96, "critical": 98}})
		res := e.issuesOf(h, "resolved")["memory|"]
		if res.ID != is.ID || res.Resolution != "reconfigured" || !strings.Contains(res.ResolutionNote, "96%") || !strings.Contains(res.ResolutionNote, "Global custom setting") {
			t.Fatalf("%+v", res)
		}
		if b := e.detail(al.ID).Alert; b.Resolution != "reconfigured" {
			t.Fatalf("%+v", b)
		}
		e.deliver()
		if p := rcv.last(); p.Event != "cleared" || p.Alert.Resolution != "reconfigured" || !strings.Contains(p.Text, "the rule changed, not the machine") {
			t.Fatalf("%s", rcv.raw[len(rcv.raw)-1])
		}
		if slices.Contains(rcv.events(), "recovered") {
			t.Fatal("no RECOVERED for a configuration-only clear")
		}
	})

	t.Run("lowering a threshold starts a fresh duration", func(t *testing.T) {
		e.points(h, 10, cpuLoad(90)) // below the 95% warning: nothing
		if e.issuesOf(h, "active")["cpu|"].ID != "" {
			t.Fatal("90% isn't an issue yet")
		}
		e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "cpu", "fields": map[string]any{"warning": 85}})
		if e.issuesOf(h, "active")["cpu|"].ID != "" {
			t.Fatal("the change itself must not open an issue for the past ten minutes")
		}
		e.points(h, 4, cpuLoad(90))
		if e.issuesOf(h, "active")["cpu|"].ID != "" {
			t.Fatal("opened before the full duration since the change")
		}
		e.points(h, 1, cpuLoad(90))
		if i := e.issuesOf(h, "active")["cpu|"]; i.ID == "" || !strings.Contains(i.Title, "85%") {
			t.Fatalf("opens after five minutes under the new rule: %+v", i)
		}
	})

	t.Run("pending timers reset on a rule change", func(t *testing.T) {
		g := e.host("db02")
		e.points(g, 3, cpuLoad(99)) // pending: 3 of 5 minutes
		e.mustPut(map[string]any{"scope": "host", "host_id": g, "check": "cpu", "fields": map[string]any{"clear": 50}})
		e.points(g, 4, cpuLoad(99))
		if e.issuesOf(g, "active")["cpu|"].ID != "" {
			t.Fatal("the pending condition kept counting across a rule change")
		}
		e.points(g, 1, cpuLoad(99))
		if e.issuesOf(g, "active")["cpu|"].ID == "" {
			t.Fatal("five minutes after the change it fires")
		}
	})

	t.Run("services: a shorter duration isn't backdated", func(t *testing.T) {
		s := e.host("svc01")
		tick := func() { // fresh telemetry, then the time-based checks
			send(t, e.h.mon, s, agentmon.KindState, stateAt(e.clk.now()))
			e.h.mon.Tick(context.Background())
		}
		e.states(s, 1, func() monitor.State { return stateAt(e.clk.now()) })
		failedAt := e.clk.now()
		e.h.mon.IngestServices(context.Background(), s, &services.Report{Supported: true, At: failedAt.UnixMilli(), Units: []services.Unit{
			{Name: "nightly.service", Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"}}}, true)
		e.mustPut(map[string]any{"scope": "host", "host_id": s, "check": "service_failed", "subject": "unit:nightly.service", "fields": map[string]any{"duration_min": 10}})
		e.clk.add(8 * time.Minute)
		tick()
		if e.issuesOf(s, "active")["service_failed|nightly.service"].ID != "" {
			t.Fatal("10 minutes not reached")
		}
		e.mustPut(map[string]any{"scope": "host", "host_id": s, "check": "service_failed", "subject": "unit:nightly.service", "fields": map[string]any{"duration_min": 1}})
		tick()
		if e.issuesOf(s, "active")["service_failed|nightly.service"].ID != "" {
			t.Fatal("the unit has been failed for 8 minutes, but the 1-minute rule is new: it must count from the change")
		}
		e.clk.add(70 * time.Second)
		tick()
		if e.issuesOf(s, "active")["service_failed|nightly.service"].ID == "" {
			t.Fatal("one minute after the change it opens")
		}
		// Disabling it for this unit closes it as disabled; another unit still alerts.
		e.h.mon.IngestServices(context.Background(), s, &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: []services.Unit{
			{Name: "nightly.service", Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"},
			{Name: "web.service", Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"}}}, true)
		e.mustPut(map[string]any{"scope": "host", "host_id": s, "check": "service_failed", "subject": "unit:nightly.service", "fields": map[string]any{"enabled": false}})
		if r := e.issuesOf(s, "resolved")["service_failed|nightly.service"]; r.Resolution != "disabled" {
			t.Fatalf("%+v", r)
		}
		e.clk.add(2 * time.Minute)
		tick()
		if e.issuesOf(s, "active")["service_failed|web.service"].ID == "" || e.issuesOf(s, "active")["service_failed|nightly.service"].ID != "" {
			t.Fatalf("per-unit: %+v", e.issuesOf(s, "active"))
		}
	})
}

func TestHealthOverridesGroupsAndPriority(t *testing.T) {
	e := newAlertEnv(t)
	h1, h2 := e.host("gpu-a"), e.host("nas-a")
	ai := e.group("AI-Hosts", h1)
	nas := e.group("NAS", h1, h2)
	e.mustPut(map[string]any{"scope": "group", "group_id": ai, "check": "cpu", "fields": map[string]any{"warning": 99}})
	e.mustPut(map[string]any{"scope": "group", "group_id": nas, "check": "cpu", "fields": map[string]any{"warning": 80}})

	// Equal priority, different values: a conflict, shown, never chosen.
	v := e.hostView(h1, "")
	w := field(t, v, "cpu", "warning")
	if w.Conflict == nil || len(w.Conflict.Groups) != 2 || num(w.Value) != 95 || !strings.Contains(w.Source.Label, "Conflict") {
		t.Fatalf("conflict: %+v", w)
	}
	if v.Summary.Conflicts != 1 || !strings.Contains(v.Summary.Text, "pack conflict") {
		t.Fatalf("summary: %+v", v.Summary)
	}
	e.points(h1, 6, cpuLoad(90))
	if e.issuesOf(h1, "active")["cpu|"].ID != "" {
		t.Fatal("conflict: the evaluator must use the next scope (95%), not either group")
	}
	if w := field(t, e.hostView(h2, ""), "cpu", "warning"); num(w.Value) != 80 || w.Source.Label != "Inherited from pack: NAS" {
		t.Fatalf("nas-a has one group: %+v", w)
	}

	// A priority decides it, visibly; the change is audited.
	if r := e.c.do("PUT", "/api/v1/monitoring/health/groups/"+nas+"/priority", map[string]any{"priority": 10}); r.status != 200 {
		t.Fatalf("priority: %d %s", r.status, r.body)
	}
	w = field(t, e.hostView(h1, ""), "cpu", "warning")
	if num(w.Value) != 80 || w.Conflict != nil || w.Source.Groups[0].Name != "NAS" {
		t.Fatalf("NAS wins at priority 10: %+v", w)
	}
	e.points(h1, 4, cpuLoad(90))
	if e.issuesOf(h1, "active")["cpu|"].ID != "" {
		t.Fatal("the priority change restarts the duration")
	}
	e.points(h1, 1, cpuLoad(90))
	if e.issuesOf(h1, "active")["cpu|"].ID == "" {
		t.Fatal("90% ≥ 80% for five minutes after the change")
	}
	// The host override beats both groups.
	e.mustPut(map[string]any{"scope": "host", "host_id": h1, "check": "cpu", "fields": map[string]any{"warning": 97}})
	if w := field(t, e.hostView(h1, ""), "cpu", "warning"); num(w.Value) != 97 || w.Source.Label != "Custom for this host" || num(w.Inherited.Value) != 80 {
		t.Fatalf("%+v", w)
	}
	if r := e.issuesOf(h1, "resolved")["cpu|"]; r.Resolution != "reconfigured" {
		t.Fatalf("90%% under 97%%: closed as a configuration change: %+v", r)
	}

	// The host changes groups through the groups page: applied at once.
	h3 := e.host("lab-a")
	lab := e.group("Lab")
	e.mustPut(map[string]any{"scope": "group", "group_id": lab, "check": "cpu", "fields": map[string]any{"enabled": false}})
	e.points(h3, 6, cpuLoad(99))
	if e.issuesOf(h3, "active")["cpu|"].ID == "" {
		t.Fatal("not in Lab yet")
	}
	if r := e.c.do("PATCH", "/api/v1/groups/"+lab, map[string]any{"host_ids": []string{h3}}); r.status != 200 {
		t.Fatalf("members: %d %s", r.status, r.body)
	}
	e.points(h3, 1, cpuLoad(99))
	if r := e.issuesOf(h3, "resolved")["cpu|"]; r.Resolution != "disabled" || !strings.Contains(r.ResolutionNote, "Inherited from pack: Lab") {
		t.Fatalf("joining Lab turned CPU off: %+v", r)
	}

	var aud listResponse[auditJSON]
	e.c.do("GET", "/api/v1/audit?limit=100", nil).json(t, &aud)
	got := map[string]auditJSON{}
	for _, a := range aud.Items {
		got[a.Action] = a
	}
	for _, want := range []string{"monitoring.health_override.create", "monitoring.health_check.disable", "monitoring.health_group.priority"} {
		if _, ok := got[want]; !ok {
			t.Errorf("not audited: %s (%v)", want, got)
		}
	}
	b, _ := json.Marshal(got["monitoring.health_check.disable"])
	if !strings.Contains(string(b), `"check":"cpu"`) || !strings.Contains(string(b), `"scope":"group"`) || !strings.Contains(string(b), `"enabled"`) {
		t.Errorf("audit details name the scope, check and fields: %s", b)
	}
}

func gpuState(clkNow time.Time, util, celsius float64, index int, amdC float64) monitor.State {
	st := stateAt(clkNow)
	st.GPUs = []monitor.GPU{{Index: index, Vendor: "nvidia", UUID: "GPU-8f3b5c2e", PCI: "0000:01:00.0", Model: "NVIDIA GeForce RTX 4090", Source: "nvidia-smi",
		UtilPercent: &util, Celsius: &celsius}}
	st.Sensors = []monitor.Sensor{{ID: "amdgpu/edge", Chip: "amdgpu", Label: "edge", Kind: "gpu", HW: "0000:03:00.0", Celsius: amdC, Critical: ptr(100.0)}}
	return st
}

func TestHealthOverridesGPUAndTemperature(t *testing.T) {
	e := newAlertEnv(t)
	h := e.host("llm01")
	ai := e.group("AI-Hosts", h)
	gpu := func(util, c, amd float64, idx int) func() monitor.State {
		return func() monitor.State { return gpuState(e.clk.now(), util, c, idx, amd) }
	}
	load := func(n int, util float64, idx int) {
		for range n {
			e.states(h, 1, gpu(util, 70, 60, idx))
			e.clk.add(-time.Minute)
			e.points(h, 1, map[string]float64{"cpu": 10, "mem": 20, "gpu.util:" + string(rune('0'+idx)): util})
		}
	}
	// Recommended: utilization isn't health. 99% for an hour is nothing.
	load(60, 99, 0)
	for k := range e.issuesOf(h, "active") {
		if strings.HasPrefix(k, "gpu_utilization") {
			t.Fatalf("GPU utilization is off by default: %v", k)
		}
	}
	v := e.hostView(h, "")
	if f := field(t, v, "gpu_utilization", "enabled"); num(f.Value) != 0 || f.Source.Label != "Recommended default" {
		t.Fatalf("%+v", f)
	}
	// Enabled for the group (a render farm where saturation means trouble).
	e.mustPut(map[string]any{"scope": "group", "group_id": ai, "check": "gpu_utilization", "fields": map[string]any{"enabled": true, "duration_min": 10}})
	load(9, 99, 0)
	if e.issuesOf(h, "active")["gpu_utilization|GPU-8f3b5c2e"].ID != "" {
		t.Fatal("fires only after its duration from the change")
	}
	load(1, 99, 0)
	if i := e.issuesOf(h, "active")["gpu_utilization|GPU-8f3b5c2e"]; i.ID == "" || !strings.Contains(i.Title, "RTX 4090") {
		t.Fatalf("enabled: %+v", e.issuesOf(h, "active"))
	}
	// After a reboot the GPU is index 1: same GPU, same issue (UUID).
	load(2, 99, 1)
	if n := len(e.issuesOf(h, "active")); n != 1 {
		t.Fatalf("a reordered GPU is the same GPU: %+v", e.issuesOf(h, "active"))
	}
	// Disabled for this GPU (an LLM server): closes as disabled, while the
	// GPU's temperature is still judged.
	sub := "gpu:GPU-8f3b5c2e"
	sv := e.hostView(h, "?subject="+sub)
	if sv.Subject == nil || sv.Subject.Strength != monsvc.IdentityStable || !slices.Contains(sv.Subject.Checks, "temperature") {
		t.Fatalf("GPU subject: %+v", sv.Subject)
	}
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "gpu_utilization", "subject": sub, "subject_label": "RTX 4090", "fields": map[string]any{"enabled": false}})
	if r := e.issuesOf(h, "resolved")["gpu_utilization|GPU-8f3b5c2e"]; r.Resolution != "disabled" {
		t.Fatalf("%+v", r)
	}
	// NVIDIA reports no temperature limit: only an explicit one applies.
	e.states(h, 5, gpu(99, 88, 85, 1))
	if _, ok := e.issuesOf(h, "active")["temperature|gpu:GPU-8f3b5c2e"]; ok {
		t.Fatal("no hardware limit and none set: no threshold is invented")
	}
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "temperature", "subject": sub, "fields": map[string]any{"warning": 83, "critical": 90}})
	f := field(t, e.hostView(h, "?subject="+sub+"&check=temperature"), "temperature", "warning")
	if num(f.Value) != 83 || f.Source.Kind != "subject" || !strings.HasPrefix(f.Source.Label, "Custom for") {
		t.Fatalf("%+v", f)
	}
	e.states(h, 3, gpu(99, 88, 85, 1))
	ti := e.issuesOf(h, "active")["temperature|gpu:GPU-8f3b5c2e"]
	if ti.ID == "" || ti.Severity != "warning" || !strings.Contains(ti.Title, "custom") {
		t.Fatalf("GPU temperature still alerts with utilization off: %+v", e.issuesOf(h, "active"))
	}
	// The AMD GPU's sensor uses its hardware limit; its source says so.
	amd := "sensor:amdgpu@0000:03:00.0/edge"
	if f := field(t, e.hostView(h, "?subject="+amd), "temperature", "critical"); num(f.Value) != 100 || f.Source.Label != "Hardware-reported limit" {
		t.Fatalf("%+v", f)
	}
	e.states(h, 3, gpu(99, 70, 101, 1))
	if ai := e.issuesOf(h, "active")["temperature|amdgpu/edge"]; ai.Severity != "critical" || strings.Contains(ai.Title, "custom") {
		t.Fatalf("hardware limit: %+v", ai)
	}
	// A custom duration on the sensor.
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "temperature", "subject": amd, "fields": map[string]any{"duration_min": 10}})
	e.states(h, 3, gpu(99, 70, 60, 1)) // cools down: recovers (a real recovery)
	if r := e.issuesOf(h, "resolved")["temperature|amdgpu/edge"]; r.Resolution != "recovered" {
		t.Fatalf("%+v", r)
	}
	e.states(h, 5, gpu(99, 70, 101, 1))
	if e.issuesOf(h, "active")["temperature|amdgpu/edge"].ID != "" {
		t.Fatal("custom 10-minute duration")
	}
	e.states(h, 6, gpu(99, 70, 101, 1))
	if e.issuesOf(h, "active")["temperature|amdgpu/edge"].ID == "" {
		t.Fatal("after 10 minutes")
	}
}

func TestHealthOverridesFilesystemsAndStorage(t *testing.T) {
	e := newAlertEnv(t)
	h := e.host("nas01")
	fs := func(mount string, pct float64) monitor.FS {
		return monitor.FS{Mount: mount, Device: "/dev/x", TotalBytes: 1000, UsedBytes: int64(pct * 10), AvailBytes: 1000 - int64(pct*10), UsedPercent: pct}
	}
	report := func(mediaMount, diskName string, failing bool) storage.Report {
		passed := !failing
		return storage.Report{Smart: true, Devices: []storage.Device{
			{Key: "wwn-0x5000c500a1b2c3d4", Name: diskName, Model: "IronWolf 8TB", Kind: "hdd", Transport: "sata", Capability: "full", Source: "smartctl",
				SMART: &storage.SMART{Passed: &passed}, Partitions: []storage.Partition{{Name: diskName + "1", UUID: "3f1c-media", Mounts: []string{mediaMount}}}},
			{Key: "nvme-eui.0025388b91b2", Name: "nvme0n1", Kind: "nvme", Transport: "nvme", Capability: "full",
				Partitions: []storage.Partition{{Name: "nvme0n1p2", UUID: "aa11-root", Mounts: []string{"/"}}}},
		}}
	}
	send(t, e.h.mon, h, agentmon.KindStorage, report("/srv/media", "sda", false))
	mounts := func(media string, mediaPct, rootPct float64) func() monitor.State {
		return func() monitor.State { return stateAt(e.clk.now(), fs("/", rootPct), fs(media, mediaPct)) }
	}
	e.states(h, 1, mounts("/srv/media", 40, 40))
	// /srv/media is bound by its filesystem UUID, / keeps the global levels.
	v := e.hostView(h, "")
	var media *monsvc.Subject
	for i, s := range v.Subjects {
		if s.Label == "/srv/media" && s.Kind == "filesystem" {
			media = &v.Subjects[i]
		}
	}
	if media == nil || media.Key != "fsuuid:3f1c-media" || media.Strength != monsvc.IdentityStable {
		t.Fatalf("subjects: %+v", v.Subjects)
	}
	e.mustPut(map[string]any{"scope": "global", "check": "filesystem", "fields": map[string]any{"warning": 85, "critical": 95}})
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "filesystem", "subject": media.Key, "subject_label": "/srv/media", "fields": map[string]any{"warning": 92, "critical": 98}})
	e.states(h, 2, mounts("/srv/media", 90, 90))
	act := e.issuesOf(h, "active")
	if act["filesystem|/"].ID == "" || act["filesystem|/srv/media"].ID != "" {
		t.Fatalf("/ at 90%% is over 85%%; /srv/media at 90%% is under its 92%%: %+v", act)
	}
	// The raised filesystem threshold doesn't weaken physical disk health.
	send(t, e.h.mon, h, agentmon.KindStorage, report("/srv/media", "sda", true))
	if act := e.issuesOf(h, "active"); act["storage_health|wwn-0x5000c500a1b2c3d4"].Severity != "critical" {
		t.Fatalf("SMART failure still alerts: %+v", act)
	}
	// The disk keeps its override across a rename (sda → sdc): same WWN.
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "media_errors", "subject": "disk:wwn-0x5000c500a1b2c3d4", "fields": map[string]any{"enabled": false}})
	send(t, e.h.mon, h, agentmon.KindStorage, report("/srv/media", "sdc", true))
	if f := field(t, e.hostView(h, "?subject=disk:wwn-0x5000c500a1b2c3d4"), "media_errors", "enabled"); num(f.Value) != 0 || f.Source.Kind != "subject" {
		t.Fatalf("%+v", f)
	}
	// Remounted elsewhere: the UUID binding follows the filesystem.
	send(t, e.h.mon, h, agentmon.KindStorage, report("/mnt/media", "sdc", true))
	e.states(h, 2, mounts("/mnt/media", 93, 50))
	act = e.issuesOf(h, "active")
	if act["filesystem|/mnt/media"].Severity != "warning" || !strings.Contains(act["filesystem|/mnt/media"].Title, "92%") {
		t.Fatalf("the UUID override applies at the new mount: %+v", act)
	}
	if r := e.issuesOf(h, "resolved")["filesystem|/"]; r.Resolution != "recovered" {
		t.Fatalf("/ at 50%%: a real recovery: %+v", r)
	}
	// The subject disappears: its issue ends as "gone", its override stays
	// (shown as unavailable) and isn't moved to anything else.
	send(t, e.h.mon, h, agentmon.KindStorage, storage.Report{Smart: true, Devices: report("/", "sdc", true).Devices[1:]})
	e.states(h, 1, func() monitor.State { return stateAt(e.clk.now(), fs("/", 50)) })
	if r := e.issuesOf(h, "resolved")["filesystem|/mnt/media"]; r.Resolution != "gone" {
		t.Fatalf("%+v", r)
	}
	v = e.hostView(h, "")
	found := false
	for _, s := range v.Subjects {
		if s.Key == "fsuuid:3f1c-media" {
			found = true
			if s.Present || !s.Customized || s.Label != "/srv/media" {
				t.Fatalf("unavailable subject: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("a configured subject that disappeared is still listed")
	}
	e.states(h, 2, func() monitor.State { return stateAt(e.clk.now(), fs("/", 50), fs("/mnt/other", 93)) })
	if act := e.issuesOf(h, "active"); !strings.Contains(act["filesystem|/mnt/other"].Title, "85%") {
		t.Fatalf("a new filesystem doesn't inherit the old one's override: %+v", act)
	}
	// It returns: its override applies again.
	send(t, e.h.mon, h, agentmon.KindStorage, report("/srv/media", "sdc", true))
	e.states(h, 2, mounts("/srv/media", 93, 50))
	if act := e.issuesOf(h, "active"); !strings.Contains(act["filesystem|/srv/media"].Title, "92%") {
		t.Fatalf("%+v", act)
	}
}

func TestHealthOverridesAPI(t *testing.T) {
	e := newAlertEnv(t)
	h, h2 := e.host("app01"), e.host("app02")
	g := e.group("Apps", h, h2)
	e.h.mkUser("otto", auth.RoleOperator)
	e.h.mkUser("vera", auth.RoleViewer)
	e.h.mkUser("aud", auth.RoleAuditor)
	op, viewer, auditor := e.h.login("otto"), e.h.login("vera"), e.h.login("aud")

	t.Run("recommended, provenance and reset to inherited", func(t *testing.T) {
		v := e.hostView(h, "")
		if !v.Summary.UsingRecommends || v.Summary.Text != "Using recommended defaults" {
			t.Fatalf("%+v", v.Summary)
		}
		e.mustPut(map[string]any{"scope": "global", "check": "filesystem", "fields": map[string]any{"warning": 80}})
		if v := e.hostView(h, ""); v.Summary.Text != "Using global custom settings" {
			t.Fatalf("%+v", v.Summary)
		}
		e.mustPut(map[string]any{"scope": "group", "group_id": g, "check": "filesystem", "fields": map[string]any{"warning": 88}})
		e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "filesystem", "fields": map[string]any{"warning": 90, "critical": 97}})
		v = e.hostView(h, "")
		if !strings.Contains(v.Summary.Text, "1 inherited pack override") || !strings.Contains(v.Summary.Text, "2 host overrides") {
			t.Fatalf("%+v", v.Summary)
		}
		w := field(t, v, "filesystem", "warning")
		if num(w.Value) != 90 || num(w.Own) != 90 || num(w.Inherited.Value) != 88 || w.Inherited.Source.Label != "Inherited from pack: Apps" {
			t.Fatalf("%+v", w)
		}
		// Reset one field: PUT the remaining sparse set.
		e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "filesystem", "fields": map[string]any{"critical": 97}})
		if w := field(t, e.hostView(h, ""), "filesystem", "warning"); num(w.Value) != 88 || w.Own != nil {
			t.Fatalf("%+v", w)
		}
		// Reset everything: an empty set removes the row.
		var out struct{ Removed bool }
		r := e.put(e.c, map[string]any{"scope": "host", "host_id": h, "check": "filesystem", "fields": map[string]any{}})
		r.json(t, &out)
		if r.status != 200 || !out.Removed {
			t.Fatalf("%d %s", r.status, r.body)
		}
		var gv monsvc.HealthView
		e.c.do("GET", "/api/v1/monitoring/health/groups/"+g, nil).json(t, &gv)
		if w := field(t, gv, "filesystem", "warning"); num(w.Value) != 88 || w.Source.Label != "Custom for this group" || num(w.Inherited.Value) != 80 {
			t.Fatalf("group view: %+v", w)
		}
		var glob monsvc.HealthView
		e.c.do("GET", "/api/v1/monitoring/health/global", nil).json(t, &glob)
		if w := field(t, glob, "filesystem", "warning"); num(w.Value) != 80 || w.Source.Label != "Global custom setting" || num(w.Inherited.Value) != 85 {
			t.Fatalf("global view: %+v", w)
		}
		if f := field(t, glob, "storage_health", "enabled"); f.Type != "bool" || len(glob.Checks) < 17 {
			t.Fatalf("capabilities: %+v", f)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for _, b := range []map[string]any{
			{"scope": "global", "check": "nope", "fields": map[string]any{"enabled": false}},
			{"scope": "global", "check": "storage_health", "fields": map[string]any{"warning": 50}},
			{"scope": "global", "check": "filesystem", "fields": map[string]any{"duration_min": 5}},
			{"scope": "host", "host_id": h, "check": "cpu", "subject": "unit:x.service", "fields": map[string]any{"enabled": false}},
			{"scope": "host", "host_id": h, "check": "filesystem", "subject": "gpu:x", "fields": map[string]any{"warning": 90}},
			{"scope": "group", "check": "cpu", "fields": map[string]any{"warning": 90}},
			{"scope": "host", "host_id": h, "check": "filesystem", "fields": map[string]any{"warning": 99, "critical": 90}},
			{"scope": "host", "host_id": h, "check": "filesystem", "fields": map[string]any{"enabled": "no"}},
		} {
			if r := e.put(e.c, b); r.status != 422 && r.status != 400 {
				t.Errorf("%v: %d %s", b, r.status, r.body)
			}
		}
		if r := e.put(e.c, map[string]any{"scope": "host", "host_id": "host_nope", "check": "cpu", "fields": map[string]any{"warning": 90}}); r.status != 404 {
			t.Errorf("unknown host: %d", r.status)
		}
	})

	t.Run("RBAC: weakening needs monitoring.settings", func(t *testing.T) {
		for _, c := range []*client{viewer, auditor} {
			if r := c.do("GET", "/api/v1/hosts/"+h+"/monitoring/health", nil); r.status != 200 {
				t.Fatalf("read: %d", r.status)
			}
			if r := e.put(c, map[string]any{"scope": "host", "host_id": h, "check": "cpu", "fields": map[string]any{"warning": 50}}); r.status != 403 {
				t.Fatalf("read-only role wrote: %d", r.status)
			}
		}
		if r := e.put(op, map[string]any{"scope": "host", "host_id": h, "check": "cpu", "fields": map[string]any{"warning": 90, "duration_min": 3}}); r.status != 200 {
			t.Fatalf("an operator may make detection stricter: %d %s", r.status, r.body)
		}
		for _, b := range []map[string]any{
			{"scope": "host", "host_id": h, "check": "cpu", "fields": map[string]any{"enabled": false}},
			{"scope": "host", "host_id": h, "check": "cpu", "fields": map[string]any{"warning": 99}},
			{"scope": "host", "host_id": h, "check": "cpu", "fields": map[string]any{"warning": 90, "duration_min": 30}},
			{"scope": "host", "host_id": h, "check": "temperature", "subject": "sensor:coretemp@coretemp.0/Package id 0", "fields": map[string]any{"enabled": false}},
			{"scope": "group", "group_id": g, "check": "cpu", "fields": map[string]any{"warning": 50}},
			{"scope": "global", "check": "cpu", "fields": map[string]any{"warning": 50}},
		} {
			if r := e.put(op, b); r.status != 403 {
				t.Errorf("operator weakened detection or changed a fleet/group setting: %v → %d %s", b, r.status, r.body)
			}
		}
		// Removing the operator's stricter override weakens: settings only.
		var ov struct{ Items []monsvc.Override }
		e.c.do("GET", "/api/v1/monitoring/health/overrides?scope=host&host_id="+h+"&check=cpu", nil).json(t, &ov)
		if r := op.do("DELETE", "/api/v1/monitoring/health/overrides/"+ov.Items[0].ID, nil); r.status != 403 {
			t.Fatalf("operator removed a stricter override: %d", r.status)
		}
		if r := op.do("PUT", "/api/v1/monitoring/health/groups/"+g+"/priority", map[string]any{"priority": 3}); r.status != 403 {
			t.Fatalf("operator changed a priority: %d", r.status)
		}
		if r := e.c.do("DELETE", "/api/v1/monitoring/health/overrides/"+ov.Items[0].ID, nil); r.status != 204 {
			t.Fatalf("admin reset: %d", r.status)
		}
		var aud listResponse[auditJSON]
		e.c.do("GET", "/api/v1/audit?action=monitoring.health_override.reset", nil).json(t, &aud)
		if len(aud.Items) == 0 {
			t.Fatal("reset audited")
		}
		b, _ := json.Marshal(aud.Items[0])
		if !strings.Contains(string(b), `"before":90`) || !strings.Contains(string(b), `"host_id":"`+h) {
			t.Fatalf("audit details: %s", b)
		}
	})

	t.Run("impact preview", func(t *testing.T) {
		big := e.group("render-farm")
		var ids []string
		for i := range 5 {
			id := e.host("render" + string(rune('a'+i)))
			ids = append(ids, id)
			util := 20.0
			if i < 3 {
				util = 99
			}
			st := stateAt(e.clk.now())
			st.GPUs = []monitor.GPU{{Index: 0, Vendor: "nvidia", UUID: "GPU-r" + string(rune('a'+i)), Model: "A6000", Source: "nvidia-smi", UtilPercent: &util}}
			send(t, e.h.mon, id, agentmon.KindState, st)
		}
		if r := e.c.do("PATCH", "/api/v1/groups/"+big, map[string]any{"host_ids": ids}); r.status != 200 {
			t.Fatal(r.body)
		}
		var pv monsvc.Preview
		r := e.c.do("POST", "/api/v1/monitoring/health/preview", map[string]any{"scope": "group", "group_id": big, "check": "gpu_utilization", "fields": map[string]any{"enabled": true}})
		if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		r.json(t, &pv)
		if pv.InScope != 5 || pv.Affected != 5 || pv.AboveBefore != 0 || pv.AboveAfter != 3 || !strings.Contains(pv.Summary, "5 hosts affected") {
			t.Fatalf("preview: %+v", pv)
		}
		e.mustPut(map[string]any{"scope": "group", "group_id": big, "check": "gpu_utilization", "fields": map[string]any{"enabled": true}})
		r = e.c.do("POST", "/api/v1/monitoring/health/preview", map[string]any{"scope": "group", "group_id": big, "check": "gpu_utilization", "fields": map[string]any{}})
		r.json(t, &pv)
		if pv.Affected != 5 || pv.AboveBefore != 3 || pv.AboveAfter != 0 || !pv.Disables {
			t.Fatalf("disable preview: %+v", pv)
		}
		// A preview changes nothing.
		if f := field(t, e.hostView(ids[0], ""), "gpu_utilization", "enabled"); num(f.Value) != 1 {
			t.Fatal("preview changed the configuration")
		}
		if r := viewer.do("POST", "/api/v1/monitoring/health/preview", map[string]any{"scope": "global", "check": "cpu", "fields": map[string]any{"warning": 90}}); r.status != 200 {
			t.Fatalf("readers can preview: %d", r.status)
		}
	})

	t.Run("notification routes are a separate thing", func(t *testing.T) {
		// A health override never touches M12 routes, and a route never
		// changes health: the two APIs are disjoint.
		var routes struct{ Routes []alerts.Route }
		e.c.do("GET", "/api/v1/notifications/routes", nil).json(t, &routes)
		if len(routes.Routes) != 1 || routes.Routes[0].ID != "nrt_default" {
			t.Fatalf("routes changed: %+v", routes.Routes)
		}
	})
}

func TestHealthLegacyMigration(t *testing.T) {
	e := newAlertEnv(t)
	h := e.host("old01")
	ctx := context.Background()
	if _, err := e.h.st.Writer.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('monitoring', ?, 0)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`,
		`{"enabled":true,"profile":"custom","custom":{"cpu_percent":90,"cpu_minutes":5,"memory_percent":90,"memory_critical_percent":97,"memory_minutes":5,"disk_warning_percent":80,"disk_critical_percent":95,"inode_warning_percent":90,"inode_critical_percent":97,"service_failed_minutes":1,"watched_stopped_minutes":2,"interface_down_minutes":1,"temperature_minutes":2,"stale_intervals":3},"retention_days":30,"sample_s":10,"slow_s":60,"services_s":60,"storage_s":600,"smart_s":21600,"buffer_kb":2048,"ssh_poll_s":300,"ssh_smart_s":3600,"ssh_sudo":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.st.Writer.Exec(`INSERT INTO mon_host_settings (host_id, settings_json, updated_at) VALUES (?, '{"watched_services":["a.service"],"probe_mounts":[],"disabled_checks":["cpu","temperature"]}', 0)`, h); err != nil {
		t.Fatal(err)
	}
	e.h.mon.Load(ctx)
	var glob monsvc.HealthView
	e.c.do("GET", "/api/v1/monitoring/health/global", nil).json(t, &glob)
	if w := field(t, glob, "cpu", "warning"); num(w.Value) != 90 || w.Source.Kind != "global" {
		t.Fatalf("custom profile → global override (only what differed): %+v", w)
	}
	if w := field(t, glob, "memory", "warning"); w.Source.Kind != "recommended" {
		t.Fatalf("unchanged fields stay Recommended: %+v", w)
	}
	v := e.hostView(h, "")
	for _, c := range []string{"cpu", "temperature"} {
		if f := field(t, v, c, "enabled"); num(f.Value) != 0 || f.Source.Kind != "host" {
			t.Fatalf("disabled check → host override: %s %+v", c, f)
		}
	}
	if s := e.h.mon.Settings(ctx); s.Profile != "" || s.Custom != nil {
		t.Fatalf("migrated once: %+v", s)
	}
	if hs := e.h.mon.HostSettings(ctx, h); len(hs.WatchedServices) != 1 {
		t.Fatalf("the rest of the host settings stay: %+v", hs)
	}
	e.h.mon.Load(ctx) // idempotent
	var ov struct{ Items []monsvc.Override }
	e.c.do("GET", "/api/v1/monitoring/health/overrides", nil).json(t, &ov)
	if len(ov.Items) != 4 { // global cpu + filesystem, host cpu + temperature
		t.Fatalf("overrides: %+v", ov.Items)
	}
}
