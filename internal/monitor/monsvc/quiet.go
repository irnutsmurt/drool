package monsvc

import (
	"context"
	"strings"
)

// Quieting an alert for good (dogfooding correction batch): the Alerts
// page's "Ignore…" offers health settings that make the condition stop
// being a problem, at the narrowest scope that does it, never by closing
// the alert by hand. Each option is an ordinary host override, written
// through the usual endpoint with its permissions and audit; the issue it
// ends closes as cleared by configuration, never as a recovery.

// OverrideChange is one override write: its key, label and fields.
type OverrideChange struct {
	OverrideKey
	SubjectLabel string `json:"subject_label,omitempty"`
	Fields       Fields `json:"fields"`
}

// QuietOption is one way to quiet a condition for good.
type QuietOption struct {
	// ID: ignore_service | stop_monitoring | expect_stopped | expect_running
	// | disable_check.
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	Effect string         `json:"effect"` // what changes, and what doesn't
	Reset  string         `json:"reset"`  // where it's undone
	Change OverrideChange `json:"change"`
}

var serviceChecks = map[string]bool{CheckServiceFailed: true, CheckServiceStopped: true, CheckServiceUnexpected: true}

// QuietOptions lists the options for a condition (a check and the issue's
// subject) on a host, and a note when there are none.
func (s *Service) QuietOptions(ctx context.Context, hostID, hostName, check, subject string) ([]QuietOption, string) {
	spec, ok := Spec(check)
	if !ok || spec.Info {
		return nil, "This kind of alert has no health check to change."
	}
	p := s.eval.current(ctx)
	if serviceChecks[check] {
		return s.quietService(p, hostID, hostName, check, subject), ""
	}
	sc, _ := s.eval.subjectOf(hostID, check, subject)
	k := OverrideKey{Scope: ScopeHost, HostID: hostID, Check: check}
	label := ""
	if spec.Subject != "" && len(sc.keys) > 0 {
		k.Subject, label = sc.keys[0], sc.label
		if label == "" {
			label = subject
		}
	}
	f := Fields{}
	if o, ok := k.find(p); ok {
		f = o.Fields.clone()
	}
	f[FieldEnabled] = 0
	what := strings.ToLower(spec.Label[:1]) + spec.Label[1:]
	o := QuietOption{ID: "disable_check", Change: OverrideChange{OverrideKey: k, SubjectLabel: label, Fields: f}}
	if k.Subject != "" {
		o.Label = "Turn off “" + spec.Label + "” for " + label + " on " + hostName
		o.Effect = "DROOL stops checking " + label + " for " + what + " on " + hostName + " only. Other hosts, " + hostName +
			"'s other " + kindPlural[spec.Subject] + " and every other check keep working."
		o.Reset = "Undo it on " + hostName + " → Monitoring → Health settings → " + label + " → Reset all to inherited."
	} else {
		o.Label = "Turn off “" + spec.Label + "” on " + hostName
		o.Effect = "DROOL stops this check on " + hostName + " only"
		if spec.Subject != "" {
			o.Effect += ", for all of its " + kindPlural[spec.Subject] + " (this one has no identity of its own to set it for)"
		}
		o.Effect += ". Other hosts and every other check keep working."
		o.Reset = "Undo it on " + hostName + " → Monitoring → Health settings → Checks for this host → " + spec.Label + " → Reset all to inherited."
	}
	return []QuietOption{o}, ""
}

var kindPlural = map[string]string{SubjectFilesystem: "filesystems", SubjectDisk: "disks", SubjectGPU: "GPUs", SubjectSensor: "sensors",
	SubjectInterface: "interfaces", SubjectService: "services", SubjectProbe: "probe targets"}

// quietService lists a service's options: ignoring it on the host; taking
// the host's own expectation away when nothing else monitors it; and the
// intentional opposite (expected stopped, or running), never guessed.
func (s *Service) quietService(p *policy, hostID, hostName, check, unit string) []QuietOption {
	k := OverrideKey{Scope: ScopeHost, HostID: hostID, Check: CheckServiceExpected, Subject: "unit:" + unit}
	exp, src := p.expected(hostID, unit)
	inh := p.resolve(hostID, CheckServiceExpected, unitCtx(unit), nil, FieldExpected, levelSubject)
	own := src.Source.Kind == "subject"
	reset := "Undo it on " + hostName + " → Monitoring → Services → " + unit + " → Customize… → Reset to inherited."
	change := func(v float64) OverrideChange {
		f := Fields{}
		if v != ExpectAuto {
			f[FieldExpected] = v
		}
		return OverrideChange{OverrideKey: k, SubjectLabel: unit, Fields: f}
	}
	var out []QuietOption
	out = append(out, QuietOption{ID: "ignore_service", Label: "Ignore " + unit + " on " + hostName,
		Effect: "Its state stays visible on " + hostName + "'s Services tab, but it never alerts there, even where a pack or every host monitors it. Other hosts aren't affected.",
		Reset:  reset, Change: change(ExpectIgnored)})
	if own && (inh.Value == nil || *inh.Value == ExpectIgnored) && exp != ExpectAuto {
		out = append(out, QuietOption{ID: "stop_monitoring", Label: "Stop monitoring " + unit + " on " + hostName,
			Effect: "Takes away " + hostName + "'s own setting for it (" + ExpectLabel(exp) + "): it's listed with its state and doesn't alert, unless a pack or every-host setting monitors it later.",
			Reset:  "Undo it by ticking Monitor for " + unit + " on " + hostName + "'s Services tab.", Change: change(ExpectAuto)})
	}
	switch {
	case exp == ExpectRunning && (check == CheckServiceFailed || check == CheckServiceStopped):
		out = append(out, QuietOption{ID: "expect_stopped", Label: "Expect " + unit + " to be stopped on " + hostName,
			Effect: "Stopped or failed is then what DROOL wants on " + hostName + ", and it alerts if " + unit + " runs. Choose this if it's meant to be off.",
			Reset:  reset, Change: change(ExpectStopped)})
	case exp == ExpectStopped && check == CheckServiceUnexpected:
		out = append(out, QuietOption{ID: "expect_running", Label: "Expect " + unit + " to run on " + hostName,
			Effect: "Running is then what DROOL wants on " + hostName + ", and it alerts if " + unit + " stops or fails. Choose this if it's meant to run.",
			Reset:  reset, Change: change(ExpectRunning)})
	}
	return out
}
