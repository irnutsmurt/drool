package hostops

import (
	"testing"

	"drool/internal/model"
)

func TestClassifySystemctlWithoutSystemd(t *testing.T) {
	for _, out := range []string{
		"System has not been booted with systemd as init system (PID 1). Can't operate.\nFailed to connect to bus: Host is down",
		"bash: line 1: systemctl: command not found",
		"sh: 1: systemctl: not found",
	} {
		if e := classifySystemctl(out); e == nil || e.Class != model.ClassConfiguration {
			t.Errorf("%q: got %v, want ConfigurationError", out, e)
		}
	}
	if e := classifySystemctl("LoadState=loaded\n"); e != nil {
		t.Errorf("normal output classified as %v", e)
	}
}
