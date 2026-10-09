package hostops

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"drool/internal/hostexec"
)

// scripted answers commands by substring (no host involved).
type scripted struct {
	answers map[string]string // substring of the command → stdout
	ran     []string
}

func (s *scripted) Run(_ context.Context, cmd string, _ hostexec.RunOptions) (*hostexec.RunResult, error) {
	s.ran = append(s.ran, cmd)
	for k, v := range s.answers {
		if strings.Contains(cmd, k) {
			return &hostexec.RunResult{Stdout: []byte(v)}, nil
		}
	}
	return &hostexec.RunResult{ExitCode: 1}, nil
}

func (s *scripted) Start(string, []byte, io.Writer, io.Writer, bool) (hostexec.Process, error) {
	return nil, errors.New("not here")
}

func TestPackageVersionsReadsOnly(t *testing.T) {
	r := &scripted{answers: map[string]string{
		"command -v": "apt-get\n",
		"dpkg-query": "curl\tii \t7.88.1-10\ndpkg\tii \t1.21.22\ntelnet\trc \t0.17\n",
	}}
	mgr, vs, err := Host{R: r}.PackageVersions(context.Background(), []string{"curl", "telnet", "htop"})
	if err != nil || mgr != "apt" {
		t.Fatal(mgr, err)
	}
	// Only "ii" counts as installed (telnet's config files remain: removed).
	if vs["curl"] != "7.88.1-10" || vs["telnet"] != "" || vs["htop"] != "" || len(vs) != 3 {
		t.Fatalf("%v", vs)
	}
	for _, c := range r.ran {
		if strings.Contains(c, "apt-get -y") || strings.Contains(c, "install") || strings.Contains(c, "update") {
			t.Fatalf("a query ran %q", c)
		}
	}
	// A query that silently produced nothing never reads as "not installed".
	broken := &scripted{answers: map[string]string{"command -v": "apt-get\n", "dpkg-query": ""}}
	if _, _, err := (Host{R: broken}).PackageVersions(context.Background(), []string{"curl"}); err == nil ||
		!strings.Contains(err.Error(), "could not read the package database") {
		t.Fatalf("broken database: %v", err)
	}
	// DNF: rpm -q.
	d := &scripted{answers: map[string]string{"command -v": "dnf\n", "rpm -q": "curl\t0:8.6.0-7.el9\nrpm\t0:4.16.1\npackage telnet is not installed\n"}}
	mgr, vs, err = Host{R: d}.PackageVersions(context.Background(), []string{"curl", "telnet"})
	if err != nil || mgr != "dnf" || vs["curl"] != "8.6.0-7.el9" || vs["telnet"] != "" {
		t.Fatal(mgr, vs, err)
	}
	// No supported package manager.
	none := &scripted{answers: map[string]string{"command -v": "none\n"}}
	if _, _, err := (Host{R: none}).PackageVersions(context.Background(), []string{"curl"}); err == nil || !strings.Contains(err.Error(), "unsupported package manager") {
		t.Fatalf("unsupported: %v", err)
	}
}
