package netmon

import (
	"net/netip"
	"testing"

	"drool/internal/firewall/policy"
)

func TestClassify(t *testing.T) {
	def, err := policy.ParseRanges(policy.DefaultInternalRanges)
	if err != nil {
		t.Fatal(err)
	}
	custom, _ := policy.ParseRanges([]string{"10.0.0.0/8", "203.0.113.0/24", "2a01:4f8:1::/48"})
	cases := []struct {
		ip       string
		internal []netip.Prefix
		scope    string
		rng      string
	}{
		{"10.1.2.3", def, ScopeInternal, "10.0.0.0/8"},
		{"172.16.5.4", def, ScopeInternal, "172.16.0.0/12"},
		{"192.168.1.25", def, ScopeInternal, "192.168.0.0/16"},
		{"100.101.102.103", def, ScopeInternal, "100.64.0.0/10"}, // CGNAT / Tailscale, internal by default
		{"fd12:3456::1", def, ScopeInternal, "fc00::/7"},         // ULA
		{"127.0.0.1", def, ScopeInternal, "127.0.0.0/8"},
		{"fe80::1", def, ScopeInternal, "fe80::/10"},
		{"8.8.8.8", def, ScopePublic, ""},
		{"2001:4860:4860::8888", def, ScopePublic, ""},
		{"::ffff:8.8.4.4", def, ScopePublic, ""},
		{"224.0.0.251", def, ScopeSpecial, "multicast"},
		{"ff02::fb", def, ScopeSpecial, "multicast"},
		{"192.0.2.10", def, ScopeSpecial, "documentation"},
		{"2001:db8::1", def, ScopeSpecial, "documentation"},
		{"198.18.0.1", def, ScopeSpecial, "benchmarking"},
		{"240.0.0.1", def, ScopeSpecial, "reserved"},
		{"0.0.0.0", def, ScopeSpecial, "unspecified"},
		{"::", def, ScopeSpecial, "unspecified"},
		// Custom ranges: the administrator's definition wins...
		{"203.0.113.7", custom, ScopeInternal, "203.0.113.0/24"},
		{"2a01:4f8:1::9", custom, ScopeInternal, "2a01:4f8:1::/48"},
		// ...but space outside them that isn't public is still never public.
		{"192.168.1.1", custom, ScopeSpecial, "private (RFC 1918)"},
		{"100.64.0.1", custom, ScopeSpecial, "shared address space (CGNAT)"},
		{"fd00::1", custom, ScopeSpecial, "unique local (IPv6 ULA)"},
		{"127.0.0.1", custom, ScopeSpecial, "loopback"},
		{"169.254.1.1", custom, ScopeSpecial, "link-local"},
		{"fe80::1", custom, ScopeSpecial, "link-local"},
		{"8.8.8.8", custom, ScopePublic, ""},
	}
	for _, c := range cases {
		got := Classify(netip.MustParseAddr(c.ip), c.internal)
		if got.Scope != c.scope || got.Range != c.rng {
			t.Errorf("%s: %+v, want %s %q", c.ip, got, c.scope, c.rng)
		}
	}
	// The most specific internal range names the address.
	nested, _ := policy.ParseRanges([]string{"10.0.0.0/8", "10.20.0.0/16"})
	if got := Classify(netip.MustParseAddr("10.20.1.1"), nested); got.Range != "10.20.0.0/16" {
		t.Errorf("nested: %+v", got)
	}
	for ip, want := range map[string]bool{"8.8.8.8": true, "10.0.0.1": false, "100.64.1.1": false, "fe80::1": false, "2001:db8::5": false, "2a00:1450::1": true} {
		if IsPublic(netip.MustParseAddr(ip)) != want {
			t.Errorf("IsPublic(%s) != %v", ip, want)
		}
	}
}

func TestSettings(t *testing.T) {
	s := DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Settings{
		{Collection: "sometimes", RetentionDays: 30, MaxFlowsPerHost: 5000},
		{Collection: CollectAll, RetentionDays: 0, MaxFlowsPerHost: 5000},
		{Collection: CollectAll, RetentionDays: 400, MaxFlowsPerHost: 5000},
		{Collection: CollectAll, RetentionDays: 30, MaxFlowsPerHost: 10},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if !s.Collects("off") || (Settings{Collection: CollectFirewall}).Collects("off") || !(Settings{Collection: CollectFirewall}).Collects("monitor") || (Settings{Collection: CollectOff}).Collects("enforce") {
		t.Error("Collects")
	}
}
