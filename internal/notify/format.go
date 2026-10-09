package notify

import (
	"fmt"
	"strings"
	"time"
)

// Headline is the first line (and email subject) of a notification.
func Headline(m Message) string {
	sev := strings.ToUpper(m.Severity)
	switch m.Kind {
	case MsgTest:
		return "TEST: DROOL notification test"
	case MsgRecovery:
		return "RECOVERED: " + m.Host
	case MsgCleared:
		if m.Resolution == "gone" {
			return "CLOSED, NO LONGER REPORTED: " + m.Host
		}
		return "CLEARED BY CONFIGURATION: " + m.Host
	case MsgEscalation:
		return sev + " (escalated): " + m.Host
	case MsgReminder:
		return "STILL ACTIVE — " + sev + ": " + m.Host
	}
	return sev + ": " + m.Host
}

// Lines is the body of a notification, one fact per line.
func Lines(m Message) []string {
	if m.Kind == MsgTest {
		out := []string{"This is a test notification from DROOL. No alert was raised.",
			"If you can read this, the destination works."}
		if m.URL != "" {
			out = append(out, "Open in DROOL: "+m.URL)
		}
		return out
	}
	var out []string
	if m.Kind == MsgCleared {
		what := "Cleared by configuration: "
		if m.Resolution == "gone" {
			what = "No longer reported: "
		}
		out = append(out, what+m.Title)
		if m.ResolutionNote != "" {
			out = append(out, "Why: "+clip(m.ResolutionNote, 300))
		}
		out = append(out, map[string]string{
			"disabled":     "This is not a recovery: the check was turned off; the condition may still be present.",
			"reconfigured": "This is not a recovery: the rule changed, not the machine.",
			"gone":         "This is not a recovery: the host no longer reports it.",
		}[m.Resolution])
		if m.DurationS != nil {
			out = append(out, "Was open for: "+Duration(time.Duration(*m.DurationS)*time.Second))
		}
		if m.AckBy != "" {
			out = append(out, ackLine(m))
		}
	} else if m.Kind == MsgRecovery {
		out = append(out, "Recovered: "+m.Title)
		if m.DurationS != nil {
			out = append(out, "Duration: "+Duration(time.Duration(*m.DurationS)*time.Second))
		}
		if m.AckBy != "" {
			out = append(out, ackLine(m))
		}
	} else {
		out = append(out, m.Title)
		if m.Detail != "" {
			out = append(out, clip(m.Detail, 300))
		}
		if m.ValueText != "" {
			out = append(out, "Current value: "+m.ValueText)
		}
		first := m.OpenedAt
		if m.ObservedAt != nil {
			first = *m.ObservedAt
		}
		out = append(out, "First detected: "+clock(first, m.TimeZone))
		if m.Kind == MsgReminder {
			out = append(out, "Still active after "+Duration(time.Since(first).Round(time.Minute))+" (reminder "+fmt.Sprint(m.Reminder)+")")
		}
		if m.AckBy != "" {
			out = append(out, ackLine(m))
		}
	}
	if m.URL != "" {
		out = append(out, "Open in DROOL: "+m.URL)
	}
	return out
}

func ackLine(m Message) string {
	l := "Acknowledged by " + m.AckBy
	if m.AckNote != "" {
		l += ": " + clip(m.AckNote, 200)
	}
	return l
}

// Text is the plain-text rendering (email body).
func Text(m Message) string { return strings.Join(Lines(m), "\n") + "\n" }

func clock(t time.Time, tz string) string {
	loc := time.Local
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// Duration formats a duration the way people say it: 42s, 7m 42s, 2h 5m,
// 3d 4h.
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd %dh", s/86400, s%86400/3600)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// oneLine removes line breaks (header values, chat titles).
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}
