package geoip_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/netmon/geoip"
	"drool/internal/netmon/geoip/mmdbtest"
)

func countryDB(t *testing.T, size int, epoch uint64) []byte {
	t.Helper()
	b, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "GeoLite2-Country", RecordSize: size, BuildEpoch: epoch}, []mmdbtest.Record{
		{Prefix: "8.0.0.0/8", Data: mmdbtest.Country("US", "United States")},
		{Prefix: "8.8.8.0/24", Data: mmdbtest.Country("US", "United States")},
		{Prefix: "81.2.69.0/24", Data: mmdbtest.Country("GB", "United Kingdom")},
		{Prefix: "185.10.0.0/16", Data: mmdbtest.Country("NL", "Netherlands")},
		{Prefix: "2001:4860::/32", Data: mmdbtest.Country("US", "United States")},
		{Prefix: "2a01:4f8::/32", Data: mmdbtest.Country("DE", "Germany")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asnDB(t *testing.T) []byte {
	t.Helper()
	b, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "GeoLite2-ASN", BuildEpoch: 1700000000}, []mmdbtest.Record{
		{Prefix: "8.8.8.0/24", Data: mmdbtest.ASN(15169, "Google LLC")},
		{Prefix: "185.10.0.0/16", Data: mmdbtest.ASN(64512, "Example Hosting B.V.")},
		{Prefix: "2001:4860::/32", Data: mmdbtest.ASN(15169, "Google LLC")},
		{Prefix: "2a01:4f8::/32", Data: mmdbtest.ASN(24940, "Hetzner Online GmbH")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReaderRecordSizes(t *testing.T) {
	for _, size := range []int{24, 28, 32} {
		db, err := geoip.Open(countryDB(t, size, 1))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		for ip, want := range map[string]string{"8.8.8.8": "US", "8.1.2.3": "US", "81.2.69.160": "GB", "2a01:4f8::1": "DE", "9.9.9.9": ""} {
			v, err := db.Lookup(netip.MustParseAddr(ip))
			if err != nil {
				t.Fatalf("size %d %s: %v", size, ip, err)
			}
			got := ""
			if m, ok := v.(map[string]any); ok {
				got = m["country"].(map[string]any)["iso_code"].(string)
			}
			if got != want {
				t.Errorf("size %d %s: %q, want %q", size, ip, got, want)
			}
		}
	}
}

func TestIPv4Database(t *testing.T) {
	b, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "Test-Country", IPVersion: 4}, []mmdbtest.Record{
		{Prefix: "1.1.1.0/24", Data: mmdbtest.Country("AU", "Australia")},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := geoip.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Lookup(netip.MustParseAddr("1.1.1.1")); v == nil {
		t.Fatal("1.1.1.1 not found")
	}
	if v, err := db.Lookup(netip.MustParseAddr("2001:db8::1")); v != nil || err != nil {
		t.Fatalf("IPv6 in an IPv4 database: %v %v", v, err)
	}
}

func TestCorruptDatabases(t *testing.T) {
	good := countryDB(t, 24, 1)
	cases := map[string][]byte{
		"empty":     {},
		"garbage":   []byte(strings.Repeat("not a database ", 100)),
		"truncated": good[len(good)/3:],
		"no tree":   good[200:],
	}
	// Flip bytes all over the file: never a panic.
	for i := 0; i < len(good); i += 7 {
		b := append([]byte(nil), good...)
		b[i] ^= 0xff
		cases["flip"+string(rune('a'+i%26))+strings.Repeat("x", i/26%3)] = b
	}
	for name, b := range cases {
		db, err := geoip.Open(b)
		if err != nil {
			continue
		}
		for _, ip := range []string{"8.8.8.8", "81.2.69.160", "2a01:4f8::1", "185.10.1.1"} {
			db.Lookup(netip.MustParseAddr(ip)) // errors are fine; panics are not
		}
		_ = name
	}
	if _, err := geoip.Open([]byte("x")); err == nil {
		t.Fatal("garbage opened")
	}
}

func write(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func TestEnricher(t *testing.T) {
	dir := t.TempDir()
	e := geoip.NewEnricher(filepath.Join(dir, "missing"), 100)
	e.Refresh()
	if e.Available() || e.Status().Error == "" {
		t.Fatalf("missing directory: %+v", e.Status())
	}
	if i := e.Lookup(netip.MustParseAddr("8.8.8.8")); !i.Empty() {
		t.Fatalf("lookup without databases: %+v", i)
	}

	e = geoip.NewEnricher(dir, 100)
	e.Refresh()
	if e.Available() {
		t.Fatal("empty directory available")
	}
	write(t, dir, "GeoLite2-Country.mmdb", countryDB(t, 24, 1700000000))
	write(t, dir, "GeoLite2-ASN.mmdb", asnDB(t))
	write(t, dir, "broken.mmdb", []byte("nope"))
	if !e.Refresh() {
		t.Fatal("refresh saw no change")
	}
	st := e.Status()
	if !st.Country || !st.ASN || len(st.Files) != 3 {
		t.Fatalf("status: %+v", st)
	}
	for _, f := range st.Files {
		if f.Name == "broken.mmdb" && f.Error == "" {
			t.Fatal("broken database without an error")
		}
	}
	got := e.Lookup(netip.MustParseAddr("8.8.8.8"))
	if got.CountryCode != "US" || got.Country != "United States" || got.ASN != 15169 || got.Org != "Google LLC" {
		t.Fatalf("8.8.8.8: %+v", got)
	}
	if got := e.Lookup(netip.MustParseAddr("2a01:4f8:1::5")); got.CountryCode != "DE" || got.ASN != 24940 {
		t.Fatalf("IPv6: %+v", got)
	}
	if got := e.Lookup(netip.MustParseAddr("::ffff:185.10.3.4")); got.CountryCode != "NL" || got.Org != "Example Hosting B.V." {
		t.Fatalf("mapped IPv4: %+v", got)
	}
	if got := e.Lookup(netip.MustParseAddr("9.9.9.9")); !got.Empty() {
		t.Fatalf("unknown address: %+v", got)
	}
	// Cached: the second lookup is a hit.
	before := e.Status().Cache
	e.Lookup(netip.MustParseAddr("8.8.8.8"))
	if after := e.Status().Cache; after.Hits != before.Hits+1 || after.Misses != before.Misses {
		t.Fatalf("cache: %+v → %+v", before, after)
	}
	if e.Refresh() {
		t.Fatal("refresh without changes reloaded")
	}

	// Replacement: a newer country database moves 8.8.8.8 and clears the cache.
	nb, _ := mmdbtest.Build(mmdbtest.Options{DatabaseType: "GeoLite2-Country", BuildEpoch: 1800000000}, []mmdbtest.Record{
		{Prefix: "8.8.8.0/24", Data: mmdbtest.Country("CA", "Canada")},
	})
	write(t, dir, "GeoLite2-Country.mmdb", nb)
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(dir, "GeoLite2-Country.mmdb"), future, future)
	if !e.Refresh() {
		t.Fatal("replacement not noticed")
	}
	if got := e.Lookup(netip.MustParseAddr("8.8.8.8")); got.CountryCode != "CA" || got.ASN != 15169 {
		t.Fatalf("after replacement: %+v", got)
	}
	// A half-written replacement keeps the last good version.
	write(t, dir, "GeoLite2-Country.mmdb", nb[:len(nb)/2])
	os.Chtimes(filepath.Join(dir, "GeoLite2-Country.mmdb"), future.Add(time.Minute), future.Add(time.Minute))
	e.Refresh()
	if got := e.Lookup(netip.MustParseAddr("8.8.8.8")); got.CountryCode != "CA" {
		t.Fatalf("after a corrupt replacement: %+v", got)
	}
	var seen bool
	for _, f := range e.Status().Files {
		if f.Name == "GeoLite2-Country.mmdb" {
			seen = strings.Contains(f.Error, "previously loaded")
		}
	}
	if !seen {
		t.Fatalf("corrupt replacement not reported: %+v", e.Status().Files)
	}
	// Removing the databases turns enrichment off.
	os.Remove(filepath.Join(dir, "GeoLite2-Country.mmdb"))
	os.Remove(filepath.Join(dir, "GeoLite2-ASN.mmdb"))
	e.Refresh()
	if e.Available() || !e.Lookup(netip.MustParseAddr("8.8.8.8")).Empty() {
		t.Fatal("databases removed but still enriching")
	}
}

func TestCacheBounded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "c.mmdb", countryDB(t, 24, 1))
	e := geoip.NewEnricher(dir, 10)
	e.Refresh()
	for i := 0; i < 100; i++ {
		e.Lookup(netip.AddrFrom4([4]byte{8, 8, byte(i), 1}))
	}
	if st := e.Status().Cache; st.Entries != 10 {
		t.Fatalf("cache entries %d, want 10", st.Entries)
	}
}

func TestIPinfoLayout(t *testing.T) {
	dir := t.TempDir()
	b, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "ipinfo lite"}, []mmdbtest.Record{
		{Prefix: "1.1.1.0/24", Data: map[string]any{"country_code": "AU", "country": "Australia", "asn": "AS13335", "as_name": "Cloudflare, Inc."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "ipinfo_lite.mmdb", b)
	e := geoip.NewEnricher(dir, 10)
	e.Refresh()
	if got := e.Lookup(netip.MustParseAddr("1.1.1.1")); got.CountryCode != "AU" || got.ASN != 13335 || got.Org != "Cloudflare, Inc." {
		t.Fatalf("ipinfo: %+v", got)
	}
}
