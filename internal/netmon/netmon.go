// Package netmon holds what network connectivity monitoring (M10) shares
// between the firewall service, which stores observations, and the
// network service, which queries and enriches them: address scopes and
// the monitoring settings. It depends on nothing but the store.
package netmon

import (
	"net/netip"
)

// Scopes of a remote address.
const (
	// ScopeInternal: inside the firewall's internal_ranges — the one
	// definition of "internal" in DROOL (DESIGN §15.3).
	ScopeInternal = "internal"
	// ScopePublic: a globally routable unicast address outside
	// internal_ranges. Only public addresses are ever enriched with
	// country and network ownership.
	ScopePublic = "public"
	// ScopeSpecial: not public and not internal: loopback, link-local,
	// multicast, documentation, private ranges an administrator removed
	// from internal_ranges, and other reserved space. Never enriched.
	ScopeSpecial = "special"
)

// Class is an address's scope and, for special and internal addresses,
// which range put it there.
type Class struct {
	Scope string `json:"scope"`
	// Range is the internal range that matched ("10.0.0.0/8") or the
	// special range's name ("loopback", "documentation", ...).
	Range string `json:"range,omitempty"`
}

type special struct {
	p    netip.Prefix
	name string
}

// specials lists non-public space that net/netip has no predicate for
// (RFC 6890 and its updates). Order matters only for naming.
var specials = func() []special {
	var out []special
	for _, s := range []struct{ cidr, name string }{
		{"0.0.0.0/8", "this network"},
		{"100.64.0.0/10", "shared address space (CGNAT)"},
		{"192.0.0.0/24", "IETF protocol assignments"},
		{"192.0.2.0/24", "documentation"},
		{"198.51.100.0/24", "documentation"},
		{"203.0.113.0/24", "documentation"},
		{"198.18.0.0/15", "benchmarking"},
		{"240.0.0.0/4", "reserved"},
		{"255.255.255.255/32", "broadcast"},
		{"2001:db8::/32", "documentation"},
		{"3fff::/20", "documentation"},
		{"2001:2::/48", "benchmarking"},
		{"100::/64", "discard-only"},
		{"fec0::/10", "site-local (deprecated)"},
		{"64:ff9b:1::/48", "local-use NAT64"},
	} {
		out = append(out, special{netip.MustParsePrefix(s.cidr), s.name})
	}
	return out
}()

var global6 = netip.MustParsePrefix("2000::/3")

// SpecialRange names a non-public range a holds, or "" for public space.
// It ignores internal_ranges.
func SpecialRange(a netip.Addr) string {
	a = a.Unmap()
	switch {
	case !a.IsValid():
		return "invalid"
	case a.IsUnspecified():
		return "unspecified"
	case a.IsLoopback():
		return "loopback"
	case a.IsLinkLocalUnicast():
		return "link-local"
	case a.IsMulticast(), a.IsLinkLocalMulticast(), a.IsInterfaceLocalMulticast():
		return "multicast"
	case a.IsPrivate():
		if a.Is4() {
			return "private (RFC 1918)"
		}
		return "unique local (IPv6 ULA)"
	}
	for _, s := range specials {
		if s.p.Contains(a) {
			return s.name
		}
	}
	if a.Is6() && !global6.Contains(a) {
		return "reserved"
	}
	return ""
}

// Classify puts a remote address in a scope. internal_ranges win, so an
// administrator can call a range internal (a VPN, 100.64.0.0/10 for
// Tailscale); anything else that isn't globally routable is special and
// is never looked up in a GeoIP database.
func Classify(a netip.Addr, internal []netip.Prefix) Class {
	a = a.Unmap()
	if !a.IsValid() {
		return Class{Scope: ScopeSpecial, Range: "invalid"}
	}
	var best netip.Prefix
	for _, p := range internal {
		if p.Contains(a) && (!best.IsValid() || p.Bits() > best.Bits()) {
			best = p
		}
	}
	if best.IsValid() {
		return Class{Scope: ScopeInternal, Range: best.String()}
	}
	if r := SpecialRange(a); r != "" {
		return Class{Scope: ScopeSpecial, Range: r}
	}
	return Class{Scope: ScopePublic}
}

// IsPublic reports whether a may be enriched (looked up in a GeoIP or ASN
// database): globally routable unicast, whatever internal_ranges say.
func IsPublic(a netip.Addr) bool { return a.IsValid() && SpecialRange(a) == "" }
