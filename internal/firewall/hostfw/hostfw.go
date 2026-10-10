// Package hostfw runs DROOL's firewall tooling on a managed host (DESIGN
// §17). The same shell tool (drool-fw), boot unit and scripts are used by
// the agent (as root, locally) and by the SSH path (through sudo), so the
// on-host transaction protocol has exactly one implementation.
package hostfw

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/hostexec"
	"drool/internal/hostops"
	"drool/internal/model"
)

//go:embed drool-fw.sh
var Tool string

//go:embed drool-firewall.service
var Unit string

//go:embed detect.sh
var detectScript string

//go:embed snapshot.sh
var snapshotScript string

// Paths are where the tooling lives on a host. Tests redirect them.
type Paths struct {
	ToolDir  string // /usr/libexec/drool-firewall
	UnitFile string // /etc/systemd/system/drool-firewall.service
	Env      string // extra environment assignments for drool-fw (tests)
}

// DefaultPaths are the real locations. /usr/libexec is bin_t under SELinux,
// so systemd runs the boot loader and rollback timer unconfined (the M7
// lesson: /usr/lib is lib_t and confined).
var DefaultPaths = Paths{ToolDir: "/usr/libexec/drool-firewall", UnitFile: "/etc/systemd/system/drool-firewall.service"}

// StateDir is the on-host state directory (DESIGN §17.0).
const StateDir = "/var/lib/drool/firewall"

// Action kinds.
const (
	Detect   = "detect"
	Status   = "status"
	Snapshot = "snapshot"
	Apply    = "apply"
	Commit   = "commit"
	Rollback = "rollback"
	Sets     = "sets"
)

// Action is one call into the host tooling (the same type travels to
// agents inside operations).
type Action = model.FirewallOp

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// ToolHash identifies the tooling version this binary installs.
func ToolHash() string { return digest(Tool) }

// Hash is the hex SHA-256 the tool computes for a file.
func Hash(s string) string { return digest(s) }

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_.:-]*$`)

const heredocTool = "DROOL_FW_TOOL_EOF"

func withNL(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

func heredoc(tag, body string) string { return "<<'" + tag + "'\n" + withNL(body) + tag + "\n" }

// install returns the script prefix that installs or updates the tool and
// the boot unit (enabled, never started: starting it would load the
// committed policy, which is what's live anyway).
func install(p Paths) string {
	var b strings.Builder
	fmt.Fprintf(&b, "D=%s\nU=%s\n", hostops.ShQuote(p.ToolDir), hostops.ShQuote(p.UnitFile))
	fmt.Fprintf(&b, "if [ \"$(sha256sum \"$D/drool-fw\" 2>/dev/null | cut -c1-64)\" != %s ]; then\n", digest(Tool))
	b.WriteString("  mkdir -p \"$D\" && chmod 755 \"$D\" || { echo '@@ERR install cannot create '\"$D\"; exit 1; }\n")
	b.WriteString("  t=$(mktemp \"$D/.drool-fw.XXXXXXXXXX\") || { echo '@@ERR install cannot write '\"$D\"; exit 1; }\n")
	b.WriteString("  cat > \"$t\" " + heredoc(heredocTool, Tool))
	b.WriteString("  chmod 755 \"$t\" && sync \"$t\" && mv -f \"$t\" \"$D/drool-fw\" && sync \"$D\" || { echo '@@ERR install cannot install drool-fw'; exit 1; }\n")
	b.WriteString("  echo '@@installed tool'\nfi\n")
	fmt.Fprintf(&b, "if [ \"$(sha256sum \"$U\" 2>/dev/null | cut -c1-64)\" != %s ]; then\n", digest(Unit))
	b.WriteString("  t=$(mktemp \"$U.XXXXXXXXXX\") || { echo '@@ERR install cannot write '\"$U\"; exit 1; }\n")
	b.WriteString("  cat > \"$t\" " + heredoc("DROOL_FW_UNIT_EOF", Unit))
	b.WriteString("  chmod 644 \"$t\" && sync \"$t\" && mv -f \"$t\" \"$U\" || { echo '@@ERR install cannot install the boot unit'; exit 1; }\n")
	b.WriteString("  systemctl daemon-reload >/dev/null 2>&1\n  echo '@@installed unit'\nfi\n")
	b.WriteString("systemctl is-enabled drool-firewall.service >/dev/null 2>&1 || systemctl enable drool-firewall.service >/dev/null 2>&1 || { echo '@@ERR install could not enable drool-firewall.service'; exit 1; }\n")
	return b.String()
}

// Script returns the shell body that performs a.
func Script(p Paths, a Action) (string, error) {
	for _, s := range []string{a.TxID, a.Epoch, a.Hash, a.RulesHash} {
		if !safeArg.MatchString(s) {
			return "", fmt.Errorf("unsafe argument %q", s)
		}
	}
	tool := hostops.ShQuote(p.ToolDir + "/drool-fw")
	env := p.Env
	if env != "" {
		env += " "
	}
	var b strings.Builder
	b.WriteString("set -u\nexport PATH=\"$PATH:/usr/sbin:/sbin\"\n")
	switch a.Kind {
	case Detect:
		b.WriteString(detectScript)
		return b.String(), nil
	case Snapshot:
		b.WriteString(snapshotScript)
		return b.String(), nil
	case Status:
		fmt.Fprintf(&b, "if [ -x %s ]; then %s%s status; else echo '@@tool_version none'; fi\n", tool, env, tool)
		return b.String(), nil
	case Apply:
		if strings.Contains(a.Candidate, "DROOL_FW_CANDIDATE_EOF") {
			return "", fmt.Errorf("invalid candidate")
		}
		b.WriteString(install(p))
		fmt.Fprintf(&b, "%s%s apply --tx %s --epoch %s --revision %d --hash %s --timeout %d --expires %d --sets-generation %d",
			env, tool, a.TxID, a.Epoch, a.Revision, a.Hash, a.TimeoutS, a.Expires.Unix(), a.SetsGeneration)
		if a.RulesHash != "" {
			fmt.Fprintf(&b, " --rules-hash %s", a.RulesHash)
		}
		if a.AllowDrift {
			b.WriteString(" --allow-drift")
		}
		b.WriteString(" " + heredoc("DROOL_FW_CANDIDATE_EOF", a.Candidate))
	case Commit:
		fmt.Fprintf(&b, "%s%s commit --tx %s --hash %s\n", env, tool, a.TxID, a.Hash)
	case Rollback:
		fmt.Fprintf(&b, "%s%s rollback --tx %s --reason %s\n", env, tool, a.TxID, hostops.ShQuote(a.Reason))
	case Sets:
		if strings.Contains(a.Delta+a.Full, "DROOL_FW_") {
			return "", fmt.Errorf("invalid set update")
		}
		b.WriteString(install(p))
		fmt.Fprintf(&b, "%s%s sets --rules-hash %s --generation %d --hash %s 3<<'DROOL_FW_DELTA_EOF' 4<<'DROOL_FW_FULL_EOF'\n",
			env, tool, a.RulesHash, a.SetsGeneration, a.Hash)
		b.WriteString(withNL(a.Delta) + "DROOL_FW_DELTA_EOF\n")
		b.WriteString(withNL(a.Full) + "DROOL_FW_FULL_EOF\n")
	default:
		return "", fmt.Errorf("unknown firewall action %q", a.Kind)
	}
	return b.String(), nil
}

// Result is the parsed output of an action.
type Result struct {
	Values   map[string]string   // first value of each @@key
	Multi    map[string][]string // every value of each @@key
	Sections map[string]string   // @@section NAME ... @@end
	ErrCode  string
	ErrMsg   string
	Exit     int
	Stderr   string
}

// Get returns a value.
func (r *Result) Get(k string) string { return r.Values[k] }

// Int returns a numeric value (0 if absent).
func (r *Result) Int(k string) int64 { n, _ := strconv.ParseInt(r.Values[k], 10, 64); return n }

// Parse reads the tool's output protocol.
func Parse(out string) *Result {
	r := &Result{Values: map[string]string{}, Multi: map[string][]string{}, Sections: map[string]string{}}
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "@@") {
			continue
		}
		k, v, _ := strings.Cut(l[2:], " ")
		switch k {
		case "section":
			var sb strings.Builder
			for i++; i < len(lines) && lines[i] != "@@end"; i++ {
				sb.WriteString(lines[i])
				sb.WriteByte('\n')
			}
			r.Sections[v] = sb.String()
		case "ERR":
			if r.ErrCode == "" {
				r.ErrCode, r.ErrMsg, _ = strings.Cut(v, " ")
			}
		default:
			if _, ok := r.Values[k]; !ok {
				r.Values[k] = v
			}
			r.Multi[k] = append(r.Multi[k], v)
		}
	}
	return r
}

// Error converts a tool error into an OpError with a class users can act on.
func (r *Result) Error() error {
	if r.ErrCode == "" {
		if r.Exit != 0 {
			msg := strings.TrimSpace(r.Stderr)
			if msg == "" {
				msg = fmt.Sprintf("exited with status %d", r.Exit)
			}
			return &model.OpError{Class: model.ClassExecution, Message: "firewall tooling failed: " + msg}
		}
		return nil
	}
	cls := model.ClassExecution
	hint := ""
	switch r.ErrCode {
	case "duplicate", "stale", "epoch", "expired", "hash", "usage", "unknown_tx", "base":
		cls = model.ClassValidation
	case "pending", "busy":
		cls = model.ClassLock
		hint = "Another firewall transaction is in progress on this host; wait for it to commit or roll back."
	case "invalid":
		cls = model.ClassValidation
		hint = "The generated policy was rejected by this host's nftables before anything changed."
	case "drift":
		cls = model.ClassValidation
		hint = "Someone changed DROOL's nftables table outside DROOL. Review the host, then apply again and choose to replace the drifted rules."
	case "timer":
		cls = model.ClassConfiguration
		hint = "The automatic rollback timer could not be armed (systemd-run); nothing was applied."
	case "rolled_back", "deadline":
		cls = model.ClassConnection
	}
	return &model.OpError{Class: cls, Message: r.ErrMsg, Hint: hint}
}

// capture keeps all of stdout (bounded) and the tail of stderr.
type capture struct{ out, errb bytes.Buffer }

type limited struct {
	b   *bytes.Buffer
	max int
}

func (w limited) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		if len(p) > room {
			w.b.Write(p[:room])
		} else {
			w.b.Write(p)
		}
	}
	return len(p), nil
}

func (c *capture) Stdout() io.Writer { return limited{&c.out, 8 << 20} }
func (c *capture) Stderr() io.Writer { return limited{&c.errb, 64 << 10} }

var _ driver.OutputSink = (*capture)(nil)

// Run performs a on a host through r, as root (b escalates on SSH hosts;
// the agent passes Become{Method: "none"} because it is root).
func Run(ctx context.Context, r hostexec.Runner, b hostops.Become, p Paths, a Action) (*Result, error) {
	body, err := Script(p, a)
	if err != nil {
		return nil, &driver.NotStartedError{Err: &model.OpError{Class: model.ClassValidation, Message: err.Error()}}
	}
	timeout := 2 * time.Minute
	if a.Kind == Detect || a.Kind == Snapshot || a.Kind == Status {
		timeout = time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := &capture{}
	sr, err := hostops.RunStreamed(cctx, r, b, "sh", body, c, nil)
	if err != nil {
		return nil, err
	}
	res := Parse(c.out.String())
	res.Exit = sr.Exit
	res.Stderr = c.errb.String()
	return res, nil
}
