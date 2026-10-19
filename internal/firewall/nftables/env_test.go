package nftables

import (
	"strings"
	"testing"
)

const debianDefault = `{"nftables": [{"metainfo": {"version": "1.0.6", "release_name": "Lester Gooch #5", "json_schema_version": 1}},
{"table": {"family": "inet", "name": "filter", "handle": 1}},
{"chain": {"family": "inet", "table": "filter", "name": "input", "handle": 1, "type": "filter", "hook": "input", "prio": 0, "policy": "accept"}},
{"chain": {"family": "inet", "table": "filter", "name": "forward", "handle": 2, "type": "filter", "hook": "forward", "prio": 0, "policy": "accept"}},
{"chain": {"family": "inet", "table": "filter", "name": "output", "handle": 3, "type": "filter", "hook": "output", "prio": 0, "policy": "accept"}},
{"table": {"family": "inet", "name": "drool", "handle": 2}},
{"chain": {"family": "inet", "table": "drool", "name": "input", "handle": 1, "type": "filter", "hook": "input", "prio": 0, "policy": "drop"}},
{"rule": {"family": "inet", "table": "drool", "chain": "input", "handle": 4, "expr": []}}]}`

const dockerIptablesNft = `{"nftables": [{"metainfo": {"version": "1.0.9"}},
{"table": {"family": "ip", "name": "filter", "handle": 3}},
{"chain": {"family": "ip", "table": "filter", "name": "FORWARD", "handle": 2, "type": "filter", "hook": "forward", "prio": 0, "policy": "drop"}},
{"chain": {"family": "ip", "table": "filter", "name": "DOCKER", "handle": 5}},
{"rule": {"family": "ip", "table": "filter", "chain": "FORWARD", "handle": 9, "expr": []}},
{"rule": {"family": "ip", "table": "filter", "chain": "FORWARD", "handle": 10, "expr": []}},
{"table": {"family": "ip", "name": "nat", "handle": 4}},
{"chain": {"family": "ip", "table": "nat", "name": "DOCKER", "handle": 1}}]}`

const firewalld = `{"nftables": [{"metainfo": {"version": "1.0.4"}},
{"table": {"family": "inet", "name": "firewalld", "handle": 1}},
{"chain": {"family": "inet", "table": "firewalld", "name": "filter_INPUT", "handle": 1, "type": "filter", "hook": "input", "prio": 10, "policy": "accept"}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT", "handle": 2, "expr": []}}]}`

func plain(extra map[string]string, nftJSON string) *Env {
	values := map[string]string{"uid": "0", "systemd": "yes", "nft": "/usr/sbin/nft", "nft_version": "nftables v1.0.6 (Lester Gooch #5)"}
	for k, v := range extra {
		values[k] = v
	}
	multi := map[string][]string{"service": {"firewalld absent", "ufw absent", "docker absent", "tailscaled absent", "nftables inactive"}}
	if s, ok := extra["service"]; ok {
		multi["service"] = append(multi["service"], s)
	}
	if s, ok := extra["missing"]; ok {
		multi["missing"] = []string{s}
	}
	if s, ok := extra["legacy"]; ok {
		multi["legacy_rules"] = []string{s}
	}
	e, _ := ParseDetect(values, multi, map[string]string{"nft_json": nftJSON})
	return e
}

func TestEligibility(t *testing.T) {
	e := plain(nil, debianDefault)
	el := e.Eligibility()
	if !el.Supported || !e.DroolTable || len(e.Tables) != 1 || e.Tables[0].Name != "filter" {
		t.Fatalf("plain nftables with Debian's empty default table: %+v %+v", el, e.Tables)
	}
	if !strings.Contains(strings.Join(el.Detected, ";"), "empty table inet filter") {
		t.Errorf("detected: %v", el.Detected)
	}
	cases := []struct {
		name  string
		extra map[string]string
		js    string
		want  string
	}{
		{"docker iptables backend", map[string]string{"docker_running": "yes"}, dockerIptablesNft, "Docker is running"},
		{"docker tables", nil, dockerIptablesNft, "table ip filter (Docker)"},
		{"firewalld", map[string]string{"service": "firewalld active"}, firewalld, "firewalld is active"},
		{"firewalld table", nil, firewalld, "inet firewalld (firewalld)"},
		{"ufw", map[string]string{"service": "ufw active", "ufw_status": "Status: active"}, "", "UFW is active"},
		{"tailscale", map[string]string{"tailscale_running": "yes"}, "", "Tailscale is running"},
		{"legacy iptables", map[string]string{"legacy": "iptables-legacy-save 4"}, "", "legacy iptables rules are present (4)"},
		{"no systemd", map[string]string{"systemd": "no"}, "", "systemd is not running"},
		{"no nft", map[string]string{"nft": "none"}, "", "nftables (nft) is not installed: DROOL can install it"},
		{"missing flock", map[string]string{"missing": "flock"}, "", "required tools are missing: flock"},
		{"iptables service", map[string]string{"service": "iptables active"}, "", "the iptables service"},
		{"unreadable", nil, "Error: Could not process rule", "could not read the nftables ruleset"},
	}
	for _, c := range cases {
		el := plain(c.extra, c.js).Eligibility()
		if el.Supported || !strings.Contains(strings.Join(el.Reasons, "\n"), c.want) {
			t.Errorf("%s: %+v", c.name, el)
		}
	}
	// Ubuntu: ufw.service is "active" (a oneshot) while UFW is disabled.
	if el := plain(map[string]string{"service": "ufw active", "ufw_status": "Status: inactive"}, "").Eligibility(); !el.Supported {
		t.Errorf("disabled UFW refused: %+v", el)
	}
	// `ufw disable` leaves UFW's iptables-nft chains loaded.
	ufwLeft := `{"nftables": [{"metainfo": {"version": "1.0.9"}},
{"table": {"family": "ip", "name": "filter", "handle": 3}},
{"chain": {"family": "ip", "table": "filter", "name": "INPUT", "handle": 1, "type": "filter", "hook": "input", "prio": 0, "policy": "accept"}},
{"chain": {"family": "ip", "table": "filter", "name": "ufw-before-input", "handle": 2}},
{"rule": {"family": "ip", "table": "filter", "chain": "INPUT", "handle": 9, "expr": []}}]}`
	if el := plain(map[string]string{"service": "ufw active", "ufw_status": "Status: inactive"}, ufwLeft).Eligibility(); el.Supported ||
		!strings.Contains(strings.Join(el.Reasons, "\n"), "UFW is disabled but its rules are still loaded") {
		t.Errorf("leftover UFW chains: %+v", el)
	}
	// ss missing only disables snapshots, not enforcement.
	if el := plain(map[string]string{"missing": "ss"}, "").Eligibility(); !el.Supported {
		t.Errorf("ss: %+v", el)
	}
	if el := plain(map[string]string{"distro_conf_flushes": "/etc/nftables.conf"}, "").Eligibility(); !el.Supported || len(el.Warnings) != 1 {
		t.Errorf("flush warning: %+v", el)
	}
}
