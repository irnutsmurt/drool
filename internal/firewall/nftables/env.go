package nftables

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Env is what detection found on a host (DESIGN §16).
type Env struct {
	Root         bool              `json:"root"`
	Systemd      bool              `json:"systemd"`
	Nft          string            `json:"nft"` // path, "" if missing
	NftVersion   string            `json:"nft_version"`
	MissingTools []string          `json:"missing_tools,omitempty"`
	Services     map[string]string `json:"services"` // name → active | inactive | absent | unknown
	UFWStatus    string            `json:"ufw_status,omitempty"`
	Docker       bool              `json:"docker"`
	Tailscale    bool              `json:"tailscale"`
	LegacyRules  int               `json:"legacy_iptables_rules"`
	ConfFlushes  bool              `json:"distro_conf_flushes"`
	Resolvers    []string          `json:"resolvers,omitempty"`
	ToolVersion  string            `json:"tool_version,omitempty"`
	Tables       []TableInfo       `json:"tables"`
	DroolTable   bool              `json:"drool_table"`
	RulesetError string            `json:"ruleset_error,omitempty"`
}

// TableInfo describes one nftables table.
type TableInfo struct {
	Family string      `json:"family"`
	Name   string      `json:"name"`
	Owner  string      `json:"owner,omitempty"` // a manager we recognise
	Chains []ChainInfo `json:"chains"`
	Rules  int         `json:"rules"`
	Sets   int         `json:"sets"`
}

// ChainInfo describes one chain.
type ChainInfo struct {
	Name   string `json:"name"`
	Hook   string `json:"hook,omitempty"` // base chains only
	Policy string `json:"policy,omitempty"`
	Prio   int    `json:"prio,omitempty"`
	Rules  int    `json:"rules"`
}

// ParseDetect builds an Env from the detect script's output.
func ParseDetect(values map[string]string, multi map[string][]string, sections map[string]string) (*Env, error) {
	e := &Env{Services: map[string]string{}}
	e.Root = values["uid"] == "0"
	e.Systemd = values["systemd"] == "yes"
	if n := values["nft"]; n != "none" {
		e.Nft = n
	}
	e.NftVersion = values["nft_version"]
	for _, t := range multi["missing"] {
		if t != "ss" { // only connection snapshots need ss
			e.MissingTools = append(e.MissingTools, t)
		}
	}
	for _, s := range multi["service"] {
		name, st, _ := strings.Cut(s, " ")
		e.Services[name] = st
	}
	e.UFWStatus = values["ufw_status"]
	e.Docker = values["docker_running"] == "yes"
	e.Tailscale = values["tailscale_running"] == "yes"
	for _, l := range multi["legacy_rules"] {
		_, n, _ := strings.Cut(l, " ")
		c, _ := strconv.Atoi(strings.TrimSpace(n))
		e.LegacyRules += c
	}
	e.ConfFlushes = values["distro_conf_flushes"] != ""
	e.Resolvers = multi["resolver"]
	e.ToolVersion = values["tool"]
	if js, ok := sections["nft_json"]; ok {
		if err := e.parseRuleset(js); err != nil {
			e.RulesetError = err.Error()
		}
	}
	return e, nil
}

func (e *Env) parseRuleset(js string) error {
	js = strings.TrimSpace(js)
	if js == "" {
		return nil
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		return fmt.Errorf("could not read the nftables ruleset: %s", firstLine(js))
	}
	tables := map[string]*TableInfo{}
	chains := map[string]*ChainInfo{}
	key := func(fam, table string) string { return fam + " " + table }
	var order []string
	for _, obj := range doc.Nftables {
		if raw, ok := obj["table"]; ok {
			var t struct{ Family, Name string }
			json.Unmarshal(raw, &t)
			k := key(t.Family, t.Name)
			tables[k] = &TableInfo{Family: t.Family, Name: t.Name}
			order = append(order, k)
		}
	}
	for _, obj := range doc.Nftables {
		if raw, ok := obj["chain"]; ok {
			var c struct {
				Family, Table, Name, Hook, Policy string
				Prio                              json.RawMessage
			}
			json.Unmarshal(raw, &c)
			t := tables[key(c.Family, c.Table)]
			if t == nil {
				continue
			}
			prio, _ := strconv.Atoi(string(c.Prio))
			t.Chains = append(t.Chains, ChainInfo{Name: c.Name, Hook: c.Hook, Policy: c.Policy, Prio: prio})
		}
	}
	for _, t := range tables {
		for i := range t.Chains {
			chains[key(t.Family, t.Name)+" "+t.Chains[i].Name] = &t.Chains[i]
		}
	}
	for _, obj := range doc.Nftables {
		if raw, ok := obj["rule"]; ok {
			var r struct{ Family, Table, Chain string }
			json.Unmarshal(raw, &r)
			if t := tables[key(r.Family, r.Table)]; t != nil {
				t.Rules++
			}
			if c := chains[key(r.Family, r.Table)+" "+r.Chain]; c != nil {
				c.Rules++
			}
		}
		if raw, ok := obj["set"]; ok {
			var s struct{ Family, Table string }
			json.Unmarshal(raw, &s)
			if t := tables[key(s.Family, s.Table)]; t != nil {
				t.Sets++
			}
		}
	}
	for _, k := range order {
		t := tables[k]
		if t.Family == "inet" && t.Name == "drool" {
			e.DroolTable = true
			continue
		}
		t.Owner = owner(t)
		e.Tables = append(e.Tables, *t)
	}
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if len(l) > 200 {
		l = l[:200]
	}
	return l
}

// owner recognises well-known managers by their tables and chains.
func owner(t *TableInfo) string {
	switch {
	case t.Name == "firewalld":
		return "firewalld"
	case strings.Contains(t.Name, "docker"):
		return "Docker"
	case t.Name == "netavark":
		return "Podman (netavark)"
	case strings.HasPrefix(t.Name, "libvirt"):
		return "libvirt"
	case strings.Contains(t.Name, "tailscale"):
		return "Tailscale"
	}
	for _, c := range t.Chains {
		switch {
		case strings.HasPrefix(c.Name, "ufw"):
			return "UFW"
		case strings.HasPrefix(c.Name, "DOCKER"):
			return "Docker"
		case strings.HasPrefix(c.Name, "ts-"):
			return "Tailscale"
		case strings.HasPrefix(c.Name, "KUBE-") || strings.HasPrefix(c.Name, "cali-") || strings.HasPrefix(c.Name, "CILIUM"):
			return "Kubernetes CNI"
		case strings.HasPrefix(c.Name, "LIBVIRT"):
			return "libvirt"
		}
	}
	if (t.Family == "ip" || t.Family == "ip6") && (t.Name == "filter" || t.Name == "nat" || t.Name == "mangle") {
		return "iptables (nft backend)"
	}
	return ""
}

// Eligibility is whether DROOL may enforce here, and why not.
type Eligibility struct {
	Supported bool     `json:"supported"`
	Reasons   []string `json:"reasons,omitempty"`  // each one blocks enforcement
	Warnings  []string `json:"warnings,omitempty"` // allowed, but worth knowing
	Detected  []string `json:"detected,omitempty"` // a summary of the environment
}

func active(st string) bool { return st == "active" }

// RemovalBlockers are what prevents removing DROOL's own table: only the
// tooling's prerequisites. Removing never touches other tables, so other
// firewall managers (installed after DROOL enforced, say) don't block it.
func (e *Env) RemovalBlockers() []string {
	var r []string
	if !e.Root {
		r = append(r, "detection could not run as root")
	}
	if !e.Systemd {
		r = append(r, "systemd is not running (the automatic rollback timer needs it)")
	}
	if e.Nft == "" {
		r = append(r, "nftables (nft) is not installed")
	}
	if len(e.MissingTools) > 0 {
		r = append(r, "required tools are missing: "+strings.Join(e.MissingTools, ", "))
	}
	return r
}

// Eligibility applies the compatibility matrix (DESIGN §16.5): only plain
// nftables with no other firewall manager is supported. Monitoring,
// simulation and explanation work everywhere; only enforcement is gated.
func (e *Env) Eligibility() Eligibility {
	var el Eligibility
	refuse := func(f string, a ...any) { el.Reasons = append(el.Reasons, fmt.Sprintf(f, a...)) }
	if !e.Root {
		refuse("detection could not run as root")
	}
	if !e.Systemd {
		refuse("systemd is not running: enforcement needs it for the boot loader (drool-firewall.service) and the automatic rollback timer")
	}
	if e.Nft == "" {
		refuse("nftables (nft) is not installed: install the nftables package (for example with a DROOL package job), then check again")
	} else {
		el.Detected = append(el.Detected, "nftables "+strings.TrimPrefix(e.NftVersion, "nftables "))
	}
	if len(e.MissingTools) > 0 {
		refuse("required tools are missing: %s", strings.Join(e.MissingTools, ", "))
	}
	if e.RulesetError != "" {
		refuse("%s", e.RulesetError)
	}
	if active(e.Services["firewalld"]) {
		refuse("firewalld is active")
	}
	// ufw.service is a oneshot that is "active (exited)" even when UFW is
	// disabled (Ubuntu cloud images): only `ufw status` says whether it is on.
	ufwOn := strings.Contains(strings.ToLower(e.UFWStatus), "status: active")
	if e.UFWStatus == "" && active(e.Services["ufw"]) {
		ufwOn = true // ufw status unavailable: be conservative
	}
	if ufwOn {
		refuse("UFW is active")
	}
	if e.Docker || active(e.Services["docker"]) {
		refuse("Docker is running: container traffic bypasses host input rules, and Docker firewall support is not available yet")
	}
	if e.Tailscale || active(e.Services["tailscaled"]) {
		refuse("Tailscale is running and manages its own firewall rules")
	}
	for _, s := range []string{"iptables", "ip6tables", "netfilter-persistent"} {
		if active(e.Services[s]) {
			refuse("the %s service loads its own firewall rules", s)
		}
	}
	if e.LegacyRules > 0 {
		refuse("legacy iptables rules are present (%d)", e.LegacyRules)
	}
	for _, t := range e.Tables {
		label := t.Family + " " + t.Name
		if t.Owner != "" {
			label += " (" + t.Owner + ")"
		}
		var hooked []string
		dropping := false
		for _, c := range t.Chains {
			if c.Hook != "" {
				hooked = append(hooked, c.Name+"@"+c.Hook)
				if c.Policy == "drop" {
					dropping = true
				}
			}
		}
		if t.Rules == 0 && !dropping {
			el.Detected = append(el.Detected, "empty table "+label)
			continue
		}
		sort.Strings(hooked)
		hint := ""
		if t.Owner == "UFW" && !ufwOn {
			// `ufw disable` leaves its chains loaded until the next boot.
			hint = " (UFW is disabled but its rules are still loaded: reboot, or remove them, then check again)"
		}
		refuse("other nftables rules are present in table %s (%d rules%s): DROOL can't prove its rules take precedence%s", label, t.Rules, hookList(hooked), hint)
	}
	for _, s := range []string{"libvirtd", "podman"} {
		if active(e.Services[s]) {
			el.Detected = append(el.Detected, s+" is running")
		}
	}
	if e.ConfFlushes {
		el.Warnings = append(el.Warnings, "/etc/nftables.conf starts with \"flush ruleset\": reloading nftables.service removes DROOL's rules until the next boot or DROOL apply (DROOL loads after it at boot)")
	}
	if e.DroolTable {
		el.Detected = append(el.Detected, "DROOL's own table (inet drool)")
	}
	el.Supported = len(el.Reasons) == 0
	return el
}

func hookList(h []string) string {
	if len(h) == 0 {
		return ""
	}
	return "; base chains " + strings.Join(h, ", ")
}
