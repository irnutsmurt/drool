// Package policy is DROOL's network intent model (DESIGN §15): who may talk
// to whom over which services. It knows nothing about nftables.
package policy

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Selector kinds.
const (
	SelHost     = "host"
	SelGroup    = "group"
	SelInternet = "internet" // everything outside internal_ranges
	SelInternal = "internal" // internal_ranges
	SelAny      = "any"      // as a destination: every managed host; as a peer: any address
	SelCIDR     = "cidr"     // a literal network
	SelNetwork  = "network"  // a named network object (one or more CIDRs)
)

// Selector is one side of a policy.
type Selector struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`   // host or group id
	CIDR string `json:"cidr,omitempty"` // for cidr
}

func (s Selector) String() string {
	switch s.Kind {
	case SelHost, SelGroup, SelNetwork:
		return s.Kind + ":" + s.ID
	case SelCIDR:
		return s.CIDR
	}
	return s.Kind
}

// Actions.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Policy is one intent: From may reach To on Services (allow), or must not
// (deny). Denies win over allows; neither can remove the management path
// (DESIGN §18).
type Policy struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`
	Action      string     `json:"action"`
	From        []Selector `json:"from"`
	To          []Selector `json:"to"`
	Services    []string   `json:"services"`
}

// Protocols in services.
const (
	TCP    = "tcp"
	UDP    = "udp"
	ICMP   = "icmp"   // IPv4
	ICMPv6 = "icmpv6" // IPv6
)

// PortRange is an inclusive range (Lo == Hi for one port).
type PortRange struct{ Lo, Hi int }

func (r PortRange) String() string {
	if r.Lo == r.Hi {
		return strconv.Itoa(r.Lo)
	}
	return fmt.Sprintf("%d-%d", r.Lo, r.Hi)
}

// Entry is one protocol/ports pair of a service. ICMP entries name a type
// (e.g. "echo-request") instead of ports.
type Entry struct {
	Protocol string `json:"protocol"`
	Ports    string `json:"ports,omitempty"` // "53", "8000-8010", "80,443"
	ICMPType string `json:"icmp_type,omitempty"`
}

// Service is a named set of protocol/ports entries.
type Service struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Builtin     bool    `json:"builtin"`
	Entries     []Entry `json:"entries"`
}

// Builtin services (DESIGN §15.2). DNS means UDP and TCP 53.
var Builtin = []Service{
	{Name: "ssh", Description: "SSH", Entries: []Entry{{Protocol: TCP, Ports: "22"}}},
	{Name: "dns", Description: "DNS (UDP and TCP)", Entries: []Entry{{Protocol: UDP, Ports: "53"}, {Protocol: TCP, Ports: "53"}}},
	{Name: "http", Description: "HTTP", Entries: []Entry{{Protocol: TCP, Ports: "80"}}},
	{Name: "https", Description: "HTTPS", Entries: []Entry{{Protocol: TCP, Ports: "443"}}},
	{Name: "ntp", Description: "NTP", Entries: []Entry{{Protocol: UDP, Ports: "123"}}},
	{Name: "postgres", Description: "PostgreSQL", Entries: []Entry{{Protocol: TCP, Ports: "5432"}}},
	{Name: "mysql", Description: "MySQL / MariaDB", Entries: []Entry{{Protocol: TCP, Ports: "3306"}}},
	{Name: "redis", Description: "Redis", Entries: []Entry{{Protocol: TCP, Ports: "6379"}}},
	{Name: "smtp", Description: "SMTP (mail transfer)", Entries: []Entry{{Protocol: TCP, Ports: "25"}}},
	{Name: "submission", Description: "Mail submission (587, 465)", Entries: []Entry{{Protocol: TCP, Ports: "587,465"}}},
	{Name: "imap", Description: "IMAP and IMAPS", Entries: []Entry{{Protocol: TCP, Ports: "143,993"}}},
	{Name: "smb", Description: "SMB / CIFS file sharing", Entries: []Entry{{Protocol: TCP, Ports: "445,139"}}},
	{Name: "nfs", Description: "NFS (with rpcbind for NFSv3)", Entries: []Entry{{Protocol: TCP, Ports: "2049,111"}, {Protocol: UDP, Ports: "2049,111"}}},
	{Name: "ping", Description: "ICMP echo (ping), IPv4 and IPv6", Entries: []Entry{{Protocol: ICMP, ICMPType: "echo-request"}, {Protocol: ICMPv6, ICMPType: "echo-request"}}},
	{Name: "mdns", Description: "Multicast DNS", Entries: []Entry{{Protocol: UDP, Ports: "5353"}}},
	{Name: "mqtt", Description: "MQTT (and MQTT over TLS)", Entries: []Entry{{Protocol: TCP, Ports: "1883,8883"}}},
	{Name: "wireguard", Description: "WireGuard", Entries: []Entry{{Protocol: UDP, Ports: "51820"}}},
}

// ICMP types a service may name.
var icmpTypes = map[string]bool{"echo-request": true, "echo-reply": true, "destination-unreachable": true, "time-exceeded": true}

// DefaultInternalRanges seeds internal_ranges (DESIGN §15.3): RFC1918,
// CGNAT/Tailscale, IPv6 ULA, link-local and loopback. Editable.
var DefaultInternalRanges = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8",
	"fc00::/7", "fe80::/10", "::1/128",
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// ParsePorts parses "53", "8000-8010", "80,443".
func ParsePorts(s string) ([]PortRange, error) {
	var out []PortRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil || a < 1 || a > 65535 {
			return nil, fmt.Errorf("invalid port %q", part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil || b < a || b > 65535 {
				return nil, fmt.Errorf("invalid port range %q", part)
			}
		}
		out = append(out, PortRange{a, b})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ports")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lo < out[j].Lo })
	return out, nil
}

// ValidateService checks a custom service.
func ValidateService(s Service) error {
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("service name %q: use lowercase letters, digits, - and _", s.Name)
	}
	for _, b := range Builtin {
		if b.Name == s.Name {
			return fmt.Errorf("%q is a built-in service", s.Name)
		}
	}
	if len(s.Entries) == 0 || len(s.Entries) > 16 {
		return fmt.Errorf("a service needs 1 to 16 protocol/port entries")
	}
	for _, e := range s.Entries {
		switch e.Protocol {
		case TCP, UDP:
			if _, err := ParsePorts(e.Ports); err != nil {
				return err
			}
		case ICMP, ICMPv6:
			if !icmpTypes[e.ICMPType] {
				return fmt.Errorf("unsupported ICMP type %q", e.ICMPType)
			}
		default:
			return fmt.Errorf("unsupported protocol %q (tcp, udp, icmp, icmpv6)", e.Protocol)
		}
	}
	return nil
}

// ParseRanges validates internal_ranges.
func ParseRanges(rs []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, r := range rs {
		p, err := netip.ParsePrefix(strings.TrimSpace(r))
		if err != nil {
			return nil, fmt.Errorf("invalid range %q", r)
		}
		out = append(out, p.Masked())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("internal_ranges can't be empty (the internet target is everything outside it)")
	}
	return out, nil
}

// Validate checks a policy's shape; references are checked by the caller.
func Validate(p Policy, services map[string]Service) error {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 80 {
		return fmt.Errorf("a policy needs a name (at most 80 characters)")
	}
	if p.Action != Allow && p.Action != Deny {
		return fmt.Errorf("action must be allow or deny")
	}
	if len(p.From) == 0 || len(p.To) == 0 {
		return fmt.Errorf("a policy needs at least one source and one destination")
	}
	if len(p.Services) == 0 {
		return fmt.Errorf("a policy needs at least one service")
	}
	check := func(side string, ss []Selector) error {
		for _, s := range ss {
			switch s.Kind {
			case SelHost, SelGroup, SelNetwork:
				if s.ID == "" {
					return fmt.Errorf("%s: %s needs an id", side, s.Kind)
				}
			case SelCIDR:
				if _, err := netip.ParsePrefix(s.CIDR); err != nil {
					return fmt.Errorf("%s: invalid network %q", side, s.CIDR)
				}
			case SelInternet, SelInternal, SelAny:
			default:
				return fmt.Errorf("%s: unknown kind %q", side, s.Kind)
			}
		}
		return nil
	}
	if err := check("from", p.From); err != nil {
		return err
	}
	if err := check("to", p.To); err != nil {
		return err
	}
	for _, n := range p.Services {
		if _, ok := services[n]; !ok {
			return fmt.Errorf("unknown service %q", n)
		}
	}
	return nil
}

// Services returns built-in plus custom services by name.
func Services(custom []Service) map[string]Service {
	m := map[string]Service{}
	for _, s := range Builtin {
		s.Builtin = true
		m[s.Name] = s
	}
	for _, s := range custom {
		m[s.Name] = s
	}
	return m
}
