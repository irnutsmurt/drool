package hostfw

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"drool/internal/hostexec"
	"drool/internal/hostops"
)

// The tool's transaction state machine, run for real under /bin/sh with
// fake nft, systemd-run and systemctl: nothing touches the machine's
// firewall. The fake nft keeps "the live DROOL table" in a file.
const fakeNft = `#!/bin/sh
case "$*" in
"-c -f "*) grep -q INVALID "$3" && { echo "Error: syntax error, unexpected INVALID" >&2; exit 1; }; exit 0 ;;
"-f -") cat > "$FAKE/in"; f="$FAKE/in" ;;
"-f "*) f=$2 ;;
"list table inet drool") [ -s "$FAKE/live" ] && exit 0; echo "Error: No such file or directory" >&2; exit 1 ;;
"-s list table inet drool") cat "$FAKE/live"; exit 0 ;;
*) echo "fake nft: unsupported: $*" >&2; exit 2 ;;
esac
if [ -f "$FAKE/fail_apply" ] && case $f in *candidate.nft) true ;; *) false ;; esac; then echo "Error: Could not process rule" >&2; exit 1; fi
if grep -q '^delete table inet drool' "$f"; then
	sed '1,/^delete table inet drool/d' "$f" > "$FAKE/live"
else
	cat "$f" >> "$FAKE/live"
fi
echo "$f" >> "$FAKE/nft.log"
`

const fakeSystemdRun = `#!/bin/sh
[ -f "$FAKE/fail_timer" ] && { echo "Failed to start transient timer unit" >&2; exit 1; }
echo "$*" >> "$FAKE/timers"
`

const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$FAKE/systemctl.log"
`

type rig struct {
	t     *testing.T
	dir   string
	fake  string
	state string
	p     Paths
}

func newRig(t *testing.T) *rig {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	for n, s := range map[string]string{"nft": fakeNft, "systemd-run": fakeSystemdRun, "systemctl": fakeSystemctl} {
		os.WriteFile(filepath.Join(bin, n), []byte(s), 0o755)
	}
	fake := filepath.Join(dir, "fake")
	os.MkdirAll(fake, 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE", fake)
	r := &rig{t: t, dir: dir, fake: fake, state: filepath.Join(dir, "state")}
	tool := filepath.Join(dir, "tool")
	r.p = Paths{ToolDir: tool, UnitFile: filepath.Join(dir, "drool-firewall.service"),
		Env: "DROOL_FW_STATE=" + r.state + " DROOL_FW_SELF=" + tool + "/drool-fw DROOL_FW_LOCK=" + filepath.Join(dir, "lock")}
	return r
}

func (r *rig) run(a Action) *Result {
	r.t.Helper()
	res, err := Run(context.Background(), hostexec.Local{}, hostops.Become{Method: "none"}, r.p, a)
	if err != nil {
		r.t.Fatalf("%s: %v", a.Kind, err)
	}
	return res
}

// tool runs drool-fw directly, like systemd does for the timer and boot.
func (r *rig) tool(args ...string) *Result {
	r.t.Helper()
	cmd := r.p.Env + " " + filepath.Join(r.p.ToolDir, "drool-fw")
	for _, a := range args {
		cmd += " " + hostops.ShQuote(a)
	}
	out, err := hostexec.Local{}.Run(context.Background(), cmd, hostexec.RunOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	res := Parse(string(out.Stdout))
	res.Exit = out.ExitCode
	return res
}

func (r *rig) live() string { b, _ := os.ReadFile(filepath.Join(r.fake, "live")); return string(b) }

func (r *rig) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.state, name))
	return string(b)
}

const epoch = "a84f3c01"

func candidate(body string) string {
	return "table inet drool\ndelete table inet drool\ntable inet drool {\n" + body + "\n}\n"
}

func apply(cand string, tx string, rev int64) Action {
	return Action{Kind: Apply, TxID: tx, Epoch: epoch, Revision: rev, Hash: Hash(cand), TimeoutS: 120,
		Expires: time.Now().Add(10 * time.Minute), Candidate: cand, RulesHash: "r" + tx[5:]}
}

func TestApplyCommit(t *testing.T) {
	r := newRig(t)
	c1 := candidate("# CANDIDATE one")
	res := r.run(apply(c1, "fwtx_one", 1))
	if res.ErrCode != "" || res.Get("state") != "pending" {
		t.Fatalf("apply: %+v", res)
	}
	if !strings.Contains(r.live(), "CANDIDATE one") {
		t.Fatal("candidate not live")
	}
	if j := r.file("transaction.json"); !strings.Contains(j, `"state": "pending"`) || !strings.Contains(j, `"revision": 1`) {
		t.Fatalf("journal: %s", j)
	}
	timers, _ := os.ReadFile(filepath.Join(r.fake, "timers"))
	if !strings.Contains(string(timers), "--on-active=120s") || !strings.Contains(string(timers), "rollback --tx fwtx_one") {
		t.Fatalf("timer: %s", timers)
	}
	if _, err := os.Stat(filepath.Join(r.p.ToolDir, "drool-fw")); err != nil {
		t.Fatal("tool not installed")
	}
	if b, _ := os.ReadFile(r.p.UnitFile); string(b) != Unit {
		t.Fatal("boot unit not installed")
	}
	// Commit with the wrong hash is refused.
	if res := r.run(Action{Kind: Commit, TxID: "fwtx_one", Hash: Hash("other")}); res.ErrCode != "hash" {
		t.Fatalf("commit wrong hash: %+v", res)
	}
	res = r.run(Action{Kind: Commit, TxID: "fwtx_one", Hash: Hash(c1)})
	if res.ErrCode != "" || res.Get("state") != "committed" {
		t.Fatalf("commit: %+v", res)
	}
	if r.file("committed.nft") != c1 || r.file("candidate.nft") != "" {
		t.Fatal("candidate not promoted")
	}
	sc, _ := os.ReadFile(filepath.Join(r.fake, "systemctl.log"))
	if !strings.Contains(string(sc), "stop drool-fw-rollback-fwtx_one.timer") {
		t.Fatalf("timer not cancelled: %s", sc)
	}
	// Committing again is idempotent; the timer firing late does nothing.
	if res := r.run(Action{Kind: Commit, TxID: "fwtx_one", Hash: Hash(c1)}); res.Get("state") != "committed" {
		t.Fatalf("second commit: %+v", res)
	}
	if res := r.tool("rollback", "--tx", "fwtx_one", "--reason", "timeout"); res.Get("state") != "committed" || !strings.Contains(r.live(), "CANDIDATE one") {
		t.Fatalf("late timer: %+v", res)
	}
	st := r.run(Action{Kind: Status})
	if st.Get("committed_revision") != "1" || st.Get("live_hash") != st.Get("expected_live") || st.Get("journal_state") != "committed" {
		t.Fatalf("status: %+v", st.Values)
	}
}

func TestRollbackPaths(t *testing.T) {
	r := newRig(t)
	c1 := candidate("# CANDIDATE committed")
	r.run(apply(c1, "fwtx_a", 1))
	r.run(Action{Kind: Commit, TxID: "fwtx_a", Hash: Hash(c1)})

	// The timer fires: the previous policy comes back.
	c2 := candidate("# CANDIDATE blocks management")
	r.run(apply(c2, "fwtx_b", 2))
	if !strings.Contains(r.live(), "blocks management") {
		t.Fatal("c2 not live")
	}
	res := r.tool("rollback", "--tx", "fwtx_b", "--reason", "timeout")
	if res.Get("state") != "rolled_back" || !strings.Contains(r.live(), "CANDIDATE committed") || strings.Contains(r.live(), "blocks") {
		t.Fatalf("timer rollback: %+v live=%q", res, r.live())
	}
	if !strings.Contains(r.file("transaction.json"), "rollback timer fired") {
		t.Fatalf("journal reason: %s", r.file("transaction.json"))
	}
	// A commit after the rollback is refused; the host keeps the old policy.
	if res := r.run(Action{Kind: Commit, TxID: "fwtx_b", Hash: Hash(c2)}); res.ErrCode != "rolled_back" {
		t.Fatalf("commit after rollback: %+v", res)
	}
	// Reboot while pending: the boot loader loads committed, never the candidate.
	c3 := candidate("# CANDIDATE pending at reboot")
	r.run(apply(c3, "fwtx_c", 2))
	os.WriteFile(filepath.Join(r.fake, "live"), nil, 0o644) // the reboot emptied the kernel's ruleset
	res = r.tool("boot")
	if res.Get("state") != "rolled_back" || !strings.Contains(r.live(), "CANDIDATE committed") {
		t.Fatalf("boot with pending: %+v live=%q", res, r.live())
	}
	if !strings.Contains(r.file("transaction.json"), "rebooted during the transaction") {
		t.Fatal("boot reason missing")
	}
	// Plain boot: the committed policy is loaded.
	os.WriteFile(filepath.Join(r.fake, "live"), nil, 0o644)
	if res := r.tool("boot"); res.Get("state") != "loaded" || !strings.Contains(r.live(), "CANDIDATE committed") {
		t.Fatalf("boot: %+v", res)
	}
	// Commit after the deadline: refused and rolled back right away.
	c4 := candidate("# CANDIDATE late")
	a := apply(c4, "fwtx_d", 2)
	a.TimeoutS = 10
	r.run(a)
	j := r.file("transaction.json")
	os.WriteFile(filepath.Join(r.state, "transaction.json"), []byte(setDeadline(j, time.Now().Add(-time.Second))), 0o600)
	if res := r.run(Action{Kind: Commit, TxID: "fwtx_d", Hash: Hash(c4)}); res.ErrCode != "deadline" || !strings.Contains(r.live(), "CANDIDATE committed") {
		t.Fatalf("late commit: %+v", res)
	}
}

func setDeadline(j string, t time.Time) string {
	lines := strings.Split(j, "\n")
	for i, l := range lines {
		if strings.Contains(l, `"deadline_unix"`) {
			lines[i] = `  "deadline_unix": ` + strconv.FormatInt(t.Unix(), 10) + ","
		}
	}
	return strings.Join(lines, "\n")
}

func TestRefusals(t *testing.T) {
	r := newRig(t)
	c1 := candidate("# CANDIDATE one")
	r.run(apply(c1, "fwtx_one", 5))
	// Another transaction while one is pending.
	if res := r.run(apply(candidate("# CANDIDATE two"), "fwtx_two", 6)); res.ErrCode != "pending" {
		t.Fatalf("concurrent: %+v", res)
	}
	r.run(Action{Kind: Commit, TxID: "fwtx_one", Hash: Hash(c1)})
	cases := []struct {
		name string
		a    Action
		code string
	}{
		{"duplicate", apply(candidate("# CANDIDATE again"), "fwtx_one", 6), "duplicate"},
		{"stale", apply(candidate("# CANDIDATE old"), "fwtx_old", 5), "stale"},
		{"foreign epoch", func() Action { a := apply(candidate("# CANDIDATE x"), "fwtx_ep", 9); a.Epoch = "ffff"; return a }(), "epoch"},
		{"expired", func() Action {
			a := apply(candidate("# CANDIDATE x"), "fwtx_exp", 9)
			a.Expires = time.Now().Add(-time.Minute)
			return a
		}(), "expired"},
		{"malformed", apply(candidate("INVALID"), "fwtx_bad", 9), "invalid"},
		{"hash mismatch", func() Action { a := apply(candidate("# CANDIDATE x"), "fwtx_hash", 9); a.Hash = Hash("x"); return a }(), "hash"},
	}
	for _, c := range cases {
		res := r.run(c.a)
		if res.ErrCode != c.code {
			t.Errorf("%s: got %q %q", c.name, res.ErrCode, res.ErrMsg)
		}
		if !strings.Contains(r.live(), "CANDIDATE one") {
			t.Fatalf("%s changed the live rules", c.name)
		}
		if res.Error() == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
	if j := r.file("transaction.json"); !strings.Contains(j, `"state": "committed"`) {
		t.Fatalf("a refusal touched the journal: %s", j)
	}
	// The timer can't be armed: nothing is applied.
	os.WriteFile(filepath.Join(r.fake, "fail_timer"), nil, 0o644)
	if res := r.run(apply(candidate("# CANDIDATE no timer"), "fwtx_nt", 9)); res.ErrCode != "timer" || strings.Contains(r.live(), "no timer") {
		t.Fatalf("no timer: %+v", res)
	}
	os.Remove(filepath.Join(r.fake, "fail_timer"))
	// nft fails while applying: the committed policy is restored at once.
	os.WriteFile(filepath.Join(r.fake, "fail_apply"), nil, 0o644)
	if res := r.run(apply(candidate("# CANDIDATE fails"), "fwtx_fail", 10)); res.ErrCode != "apply" {
		t.Fatalf("apply failure: %+v", res)
	}
	os.Remove(filepath.Join(r.fake, "fail_apply"))
	if !strings.Contains(r.live(), "CANDIDATE one") || !strings.Contains(r.file("transaction.json"), "rolled_back") {
		t.Fatalf("after a failed apply: live=%q", r.live())
	}
	// Drift: someone edited DROOL's table. Refused unless explicitly allowed.
	os.WriteFile(filepath.Join(r.fake, "live"), []byte("something else\n"), 0o644)
	if st := r.run(Action{Kind: Status}); st.Get("live_hash") == st.Get("expected_live") {
		t.Fatal("status did not show drift")
	}
	if res := r.run(apply(candidate("# CANDIDATE fix"), "fwtx_fix", 11)); res.ErrCode != "drift" {
		t.Fatalf("drift: %+v", res)
	}
	a := apply(candidate("# CANDIDATE fix"), "fwtx_fix2", 11)
	a.AllowDrift = true
	if res := r.run(a); res.Get("state") != "pending" {
		t.Fatalf("allow drift: %+v", res)
	}
	// A rollback now restores DROOL's last committed policy, not the drift.
	r.tool("rollback", "--tx", "fwtx_fix2", "--reason", "timeout")
	if !strings.Contains(r.live(), "CANDIDATE one") {
		t.Fatalf("rollback after drift: %q", r.live())
	}
}

func TestCrashBeforeApplyAndSets(t *testing.T) {
	r := newRig(t)
	c1 := candidate("# CANDIDATE base\n  set g_web_4 { type ipv4_addr; }")
	a := apply(c1, "fwtx_base", 1)
	r.run(a)
	r.run(Action{Kind: Commit, TxID: "fwtx_base", Hash: Hash(c1)})

	// A crash right after the journal was written (before the rules
	// changed): the timer or the boot loader restores, which is a no-op.
	c2 := candidate("# CANDIDATE crashed")
	os.MkdirAll(r.state, 0o700)
	j := strings.ReplaceAll(r.file("transaction.json"), `"state": "committed"`, `"state": "pending"`)
	j = strings.ReplaceAll(j, "fwtx_base", "fwtx_crash")
	os.WriteFile(filepath.Join(r.state, "transaction.json"), []byte(j), 0o600)
	os.WriteFile(filepath.Join(r.state, "candidate.nft"), []byte(c2), 0o600)
	if res := r.tool("rollback", "--tx", "fwtx_crash", "--reason", "timeout"); res.Get("state") != "rolled_back" || !strings.Contains(r.live(), "CANDIDATE base") {
		t.Fatalf("crash recovery: %+v", res)
	}

	// Set updates change elements only, and only on the committed rules.
	full := candidate("# CANDIDATE base\n  set g_web_4 { type ipv4_addr; elements = { 10.0.0.1 } }")
	s := Action{Kind: Sets, RulesHash: "rbase", SetsGeneration: 3, Hash: Hash(full),
		Delta: "flush set inet drool g_web_4\nadd element inet drool g_web_4 { 10.0.0.1 }\n", Full: full}
	res := r.run(s)
	if res.ErrCode != "" || res.Get("sets_generation") != "3" {
		t.Fatalf("sets: %+v %s", res, res.Stderr)
	}
	if r.file("committed.nft") != full || !strings.Contains(r.live(), "add element inet drool g_web_4 { 10.0.0.1 }") {
		t.Fatalf("sets result: %q", r.file("committed.nft"))
	}
	if st := r.run(Action{Kind: Status}); st.Get("live_hash") != st.Get("expected_live") || st.Get("sets_generation") != "3" {
		t.Fatalf("status after sets: %+v", st.Values)
	}
	// Older generations and other rules are ignored or refused.
	s.SetsGeneration = 2
	if res := r.run(s); res.Get("sets_generation") != "3" {
		t.Fatalf("old generation: %+v", res)
	}
	s.SetsGeneration, s.RulesHash = 4, "rother"
	if res := r.run(s); res.ErrCode != "base" {
		t.Fatalf("other rules: %+v", res)
	}
}

func TestParse(t *testing.T) {
	r := Parse("noise\n@@a 1\n@@a 2\n@@section x\nline1\nline2\n@@end\n@@ERR stale too old\n")
	if r.Get("a") != "1" || len(r.Multi["a"]) != 2 || r.Sections["x"] != "line1\nline2\n" || r.ErrCode != "stale" || r.ErrMsg != "too old" {
		t.Fatalf("%+v", r)
	}
}
