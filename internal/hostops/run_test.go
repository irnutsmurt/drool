package hostops

import (
	"strings"
	"testing"
)

// Remote scripts run under dash on Debian/Ubuntu. dash's kill builtin rejects
// "--" before a negative process group id, which once left cancelled sudo
// jobs running. Guard against reintroducing that form.
func TestRemoteScriptsAvoidDashIncompatibleKill(t *testing.T) {
	for _, bad := range []string{"kill -- ", "-- \"-$p\"", "kill -s "} {
		if strings.Contains(killScript, bad) {
			t.Errorf("killScript contains %q, which dash rejects", bad)
		}
	}
}

func TestRunDirPattern(t *testing.T) {
	for _, ok := range []string{"/tmp/drool.AbCdEf1234", "/run/user/1000/drool.0123456789"} {
		if !runDirRE.MatchString(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "/tmp/drool.", "/tmp/x; rm -rf /", "relative/drool.AbCdEf1234", "/tmp/drool.AbCdEf1234\n"} {
		if runDirRE.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShQuote(t *testing.T) {
	if got := ShQuote(`a'b`); got != `'a'\''b'` {
		t.Fatalf("ShQuote: %s", got)
	}
}
