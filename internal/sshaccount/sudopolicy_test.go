package sshaccount

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stand-ins for the host's sudo tools, so that sudoPolicy runs here as it
// does on a host (the integration tests run it against real sudo and
// sudo-rs). visudo -cf rejects a file with a line containing $FAKE_REJECT,
// the way sudo-rs rejects requiretty, and logs every file it is shown;
// visudo -c reports the problems in $FAKE_GLOBAL, plus $FAKE_WORSE once
// DROOL's file is installed. sudo, run as the account through runuser and
// setsid, wants a terminal when $FAKE_TTY is set (unless DROOL's file has
// an exception), and runs id -u when DROOL's file grants the rule; it always
// warns about the host name, as sudo does on many hosts.
var policyFakes = map[string]string{
	"visudo": `#!/bin/sh
if [ "$1" = -cf ]; then
	{ echo "=="; cat "$2"; } >>"$FAKE_LOG"
	if [ -n "${FAKE_REJECT:-}" ] && grep -q -- "$FAKE_REJECT" "$2"; then
		echo "$2:4:17: syntax error: unknown setting: '$FAKE_REJECT'"
		echo "visudo: invalid sudoers file"
		exit 1
	fi
	echo "$2: parsed OK"
	exit 0
fi
[ "$1" = -c ] || exit 2
bad=0
if [ -s "${FAKE_GLOBAL:-/nonexistent}" ]; then cat "$FAKE_GLOBAL"; bad=1; fi
if [ -n "${FAKE_WORSE:-}" ] && [ -f "$FAKE_S" ]; then echo "$FAKE_WORSE"; bad=1; fi
echo "/etc/sudoers: parsed OK"
exit $bad
`,
	"setsid": `#!/bin/sh
[ "$1" = -w ] && shift
exec "$@"
`,
	"runuser": `#!/bin/sh
[ "$1" = -u ] && [ "$3" = -- ] || exit 2
shift 3
exec "$@"
`,
	"sudo": `#!/bin/sh
[ "$*" = "-n -- id -u" ] || { echo "unexpected: sudo $*" >&2; exit 2; }
echo "sudo: unable to resolve host test-host: Name or service not known" >&2
if [ -n "${FAKE_TTY:-}" ] && ! grep -q '!requiretty' "$FAKE_S" 2>/dev/null; then
	echo "sudo: sorry, you must have a tty to run sudo" >&2
	exit 1
fi
if [ -n "${FAKE_DENY:-}" ] || ! grep -q 'NOPASSWD: ALL' "$FAKE_S" 2>/dev/null; then
	echo "sudo: a password is required" >&2
	exit 1
fi
echo 0
`,
}

type policyRun struct {
	vals          map[string]string
	code, msg     string
	sudoers       string // DROOL's file afterwards ("" if there is none)
	shown         string // every file visudo -cf was shown
	leftover      []string
	dir, sudoers0 string
}

// runPolicy runs sudoPolicy for the account drool. before is DROOL's file
// before the run ("": none); pre is what visudo -c reported before ("": it
// passed).
func runPolicy(t *testing.T, before, pre string, env ...string) policyRun {
	t.Helper()
	dir := t.TempDir()
	bin, sd := filepath.Join(dir, "bin"), filepath.Join(dir, "sudoers.d")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(sd, 0o755)
	for name, body := range policyFakes {
		os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755)
	}
	s := filepath.Join(sd, "drool-account-drool")
	if before != "" {
		os.WriteFile(s, []byte(before), 0o440)
	}
	preFile, global := filepath.Join(dir, "pre"), filepath.Join(dir, "global")
	preOK := "1"
	if pre != "" {
		preOK = "0"
		os.WriteFile(preFile, []byte(pre+"\n"), 0o600)
		os.WriteFile(global, []byte(pre+"\n"), 0o600)
	} else {
		os.WriteFile(preFile, nil, 0o600)
	}
	script := strings.Join([]string{
		"set -u",
		"U=drool",
		"S=" + s,
		"SD=" + sd,
		"pre_ok=" + preOK,
		"PRE=" + preFile,
		`err() { printf '@@ERR %s %s\n' "$1" "$2"; exit 1; }`,
		`kv() { printf '@@%s %s\n' "$1" "$2"; }`,
		sudoPolicy,
		"kv done 1",
	}, "\n")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "FAKE_S=" + s, "FAKE_LOG=" + filepath.Join(dir, "shown"), "FAKE_GLOBAL=" + global}, env...)
	out, _ := cmd.CombinedOutput()
	r := policyRun{dir: dir, sudoers0: before}
	r.vals, r.code, r.msg = parseOutput(out)
	if b, err := os.ReadFile(s); err == nil {
		r.sudoers = string(b)
	}
	shown, _ := os.ReadFile(filepath.Join(dir, "shown"))
	r.shown = string(shown)
	entries, _ := os.ReadDir(sd)
	for _, e := range entries {
		if e.Name() != "drool-account-drool" {
			r.leftover = append(r.leftover, e.Name())
		}
	}
	if r.code == "" && r.vals["done"] != "1" {
		t.Fatalf("the policy didn't finish:\n%s", out)
	}
	return r
}

const (
	minimalRule = "drool ALL=(ALL:ALL) NOPASSWD: ALL\n"
	ttyRule     = "Defaults:drool !requiretty\ndrool ALL=(ALL:ALL) NOPASSWD: ALL\n"
	oldRule     = "# Managed by DROOL\nDefaults:drool !requiretty\ndrool ALL=(ALL:ALL) NOPASSWD: ALL\n"
)

// rules is DROOL's file without its comments.
func rules(s string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(s, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			b.WriteString(l)
		}
	}
	return b.String()
}

func TestSudoPolicy(t *testing.T) {
	t.Run("no terminal needed: the minimal rule and nothing else", func(t *testing.T) {
		r := runPolicy(t, "", "")
		if r.code != "" || rules(r.sudoers) != minimalRule || r.vals["sudoers_changed"] != "1" || r.vals["sudo_tty"] != "" || r.vals["sudo_exception"] != "" {
			t.Fatalf("%+v", r)
		}
		if !strings.Contains(r.sudoers, "root-equivalent") || r.vals["sudo_rule"] != "drool ALL=(ALL:ALL) NOPASSWD: ALL;" {
			t.Fatalf("file %q, reported %q", r.sudoers, r.vals["sudo_rule"])
		}
		if strings.Contains(r.shown, "requiretty") {
			t.Fatalf("a requiretty exception was proposed although sudo didn't need one:\n%s", r.shown)
		}
	})
	// The reported host: sudo-rs, whose visudo rejects requiretty. It
	// doesn't need the exception, so DROOL never proposes it.
	t.Run("visudo rejects requiretty, and sudo doesn't need it", func(t *testing.T) {
		r := runPolicy(t, "", "", "FAKE_REJECT=requiretty")
		if r.code != "" || rules(r.sudoers) != minimalRule || strings.Contains(r.shown, "requiretty") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("sudo requires a terminal and visudo accepts the exception", func(t *testing.T) {
		r := runPolicy(t, "", "", "FAKE_TTY=1")
		if r.code != "" || rules(r.sudoers) != ttyRule || r.vals["sudo_tty"] != "required" || r.vals["sudo_exception"] != "requiretty" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("sudo requires a terminal and visudo rejects the exception: refused, nothing installed", func(t *testing.T) {
		r := runPolicy(t, "", "", "FAKE_TTY=1", "FAKE_REJECT=requiretty")
		if r.code != "sudotty" || !strings.Contains(r.msg, "requires a terminal for drool") || !strings.Contains(r.msg, "unknown setting") || r.sudoers != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("visudo rejects the rule itself: never installed", func(t *testing.T) {
		r := runPolicy(t, "", "", "FAKE_REJECT=NOPASSWD")
		if r.code != "sudoers" || !strings.HasPrefix(r.msg, "visudo rejected DROOL's sudo rule, so it wasn't installed") || r.sudoers != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("the account's sudo doesn't work: taken out again", func(t *testing.T) {
		r := runPolicy(t, "", "", "FAKE_DENY=1")
		if r.code != "sudoers" || !strings.Contains(r.msg, "a password is required") || !strings.Contains(r.msg, "took its rule out again") || r.sudoers != "" {
			t.Fatalf("%+v", r)
		}
		if strings.Contains(r.msg, "resolve host") && !strings.Contains(r.msg, "password is required") {
			t.Fatalf("the reason is missing: %s", r.msg)
		}
	})
	t.Run("an earlier rule is put back exactly when the new one fails", func(t *testing.T) {
		r := runPolicy(t, oldRule, "", "FAKE_DENY=1")
		if r.code != "sudoers" || r.sudoers != oldRule {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("an earlier DROOL rule with the exception becomes the minimal one", func(t *testing.T) {
		r := runPolicy(t, oldRule, "")
		if r.code != "" || rules(r.sudoers) != minimalRule || r.vals["sudoers_changed"] != "1" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("the same rule again: nothing changes", func(t *testing.T) {
		first := runPolicy(t, "", "")
		r := runPolicy(t, first.sudoers, "")
		if r.code != "" || r.sudoers != first.sudoers || r.vals["sudoers_changed"] != "" {
			t.Fatalf("%+v", r)
		}
	})
	// Problems the host already had (another file's mode) aren't DROOL's,
	// and don't stop a rule that works.
	t.Run("problems already on the host aren't blamed on DROOL", func(t *testing.T) {
		r := runPolicy(t, "", "/etc/sudoers.d/deploy: bad permissions, should be mode 0440")
		if r.code != "" || rules(r.sudoers) != minimalRule {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a configuration that validated doesn't with DROOL's rule: taken out", func(t *testing.T) {
		r := runPolicy(t, "", "", "FAKE_WORSE=/etc/sudoers.d/drool-account-drool: a new problem")
		if r.code != "sudoers" || !strings.Contains(r.msg, "a new problem") || r.sudoers != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a new problem on top of old ones: taken out", func(t *testing.T) {
		r := runPolicy(t, oldRule, "/etc/sudoers.d/deploy: bad permissions, should be mode 0440",
			"FAKE_WORSE=/etc/sudoers.d/drool-account-drool: a new problem")
		if r.code != "sudoers" || !strings.Contains(r.msg, "a new problem") || strings.Contains(r.msg, "deploy: bad permissions") || r.sudoers != oldRule {
			t.Fatalf("%+v", r)
		}
	})
}

// Whatever happens, no proposed file is left behind in the sudoers
// directory, where a later sudo version might read it.
func TestSudoPolicyLeavesNoProposals(t *testing.T) {
	for _, env := range [][]string{nil, {"FAKE_TTY=1"}, {"FAKE_TTY=1", "FAKE_REJECT=requiretty"}, {"FAKE_REJECT=NOPASSWD"}, {"FAKE_DENY=1"},
		{"FAKE_WORSE=/etc/sudoers.d/drool-account-drool: new"}} {
		for _, before := range []string{"", oldRule} {
			if r := runPolicy(t, before, "", env...); len(r.leftover) > 0 {
				t.Errorf("%v (before: %q): left %v", env, before, r.leftover)
			}
		}
	}
}
