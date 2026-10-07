// Package geoip enriches public IP addresses with country and network
// ownership (ASN) from local databases (M10). It never contacts an online
// service: lookups read files an operator installs and updates separately
// (geoipupdate, DB-IP Lite downloads, ...). The values are context, not a
// verdict, and may be inaccurate or stale.
package geoip

import (
	"container/list"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

// Info is what DROOL knows about a public address. Empty fields are
// unknown.
type Info struct {
	CountryCode string `json:"country_code,omitempty"` // ISO 3166-1 alpha-2
	Country     string `json:"country,omitempty"`
	ASN         uint32 `json:"asn,omitempty"`
	Org         string `json:"org,omitempty"` // the AS's organization
}

// Empty reports whether nothing is known.
func (i Info) Empty() bool { return i.CountryCode == "" && i.ASN == 0 }

// Provider looks addresses up. Implementations are safe for concurrent
// use; the controller can replace the MMDB backend without touching
// callers.
type Provider interface {
	Lookup(a netip.Addr) (Info, error)
}

// Kinds of data a database provides.
const (
	KindCountry = "country"
	KindASN     = "asn"
)

// mmdbProvider reads one MMDB file. It recognises the record layouts of
// MaxMind GeoLite2/GeoIP2 Country/City/ASN, DB-IP Lite country/ASN and
// IPinfo Lite (country and ASN in one database).
type mmdbProvider struct {
	db    *DB
	kinds []string
}

// detectKinds probes a database for country and ASN fields, from its type
// and a few well-known public addresses.
func detectKinds(db *DB) []string {
	has := map[string]bool{}
	t := strings.ToLower(db.Meta().DatabaseType)
	if strings.Contains(t, "asn") || strings.Contains(t, "-as") {
		has[KindASN] = true
	}
	if strings.Contains(t, "country") || strings.Contains(t, "city") {
		has[KindCountry] = true
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "9.9.9.9", "2001:4860:4860::8888", "81.2.69.160"} {
		v, err := db.Lookup(netip.MustParseAddr(s))
		if err != nil || v == nil {
			continue
		}
		i := decodeInfo(v)
		if i.CountryCode != "" {
			has[KindCountry] = true
		}
		if i.ASN != 0 {
			has[KindASN] = true
		}
	}
	var out []string
	for _, k := range []string{KindCountry, KindASN} {
		if has[k] {
			out = append(out, k)
		}
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// decodeInfo maps the record layouts DROOL understands onto Info.
func decodeInfo(v any) Info {
	m, ok := v.(map[string]any)
	if !ok {
		return Info{}
	}
	var i Info
	// MaxMind / DB-IP: country.iso_code, country.names.en.
	for _, k := range []string{"country", "registered_country"} {
		if c, ok := m[k].(map[string]any); ok && i.CountryCode == "" {
			i.CountryCode = str(c, "iso_code")
			if n, ok := c["names"].(map[string]any); ok {
				i.Country = str(n, "en")
			}
		}
	}
	// IPinfo Lite: country_code, country.
	if i.CountryCode == "" {
		i.CountryCode = str(m, "country_code")
		i.Country = str(m, "country")
	}
	// MaxMind / DB-IP ASN.
	if n, ok := m["autonomous_system_number"]; ok {
		i.ASN = uint32(asUint(n))
		i.Org = str(m, "autonomous_system_organization")
	}
	// IPinfo Lite: asn "AS15169", as_name.
	if i.ASN == 0 {
		if s := str(m, "asn"); strings.HasPrefix(strings.ToUpper(s), "AS") {
			n, _ := strconv.ParseUint(s[2:], 10, 32)
			i.ASN = uint32(n)
			i.Org = str(m, "as_name")
		}
	}
	i.CountryCode = strings.ToUpper(i.CountryCode)
	if len(i.CountryCode) != 2 {
		i.CountryCode, i.Country = "", ""
	}
	i.Org = clean(i.Org, 120)
	i.Country = clean(i.Country, 80)
	return i
}

// clean keeps printable text only: database contents end up in the UI.
func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f && r != '�' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func (p *mmdbProvider) Lookup(a netip.Addr) (Info, error) {
	v, err := p.db.Lookup(a)
	if err != nil || v == nil {
		return Info{}, err
	}
	return decodeInfo(v), nil
}

// --- cache ---

// Cache is a bounded LRU of lookups in front of a provider. Generation
// changes (a database was replaced) invalidate it.
type Cache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[netip.Addr]*list.Element
	gen   uint64

	hits, misses uint64
}

type cacheEntry struct {
	a    netip.Addr
	info Info
}

// NewCache returns a cache holding at most max addresses.
func NewCache(max int) *Cache {
	return &Cache{max: max, ll: list.New(), items: map[netip.Addr]*list.Element{}}
}

func (c *Cache) get(a netip.Addr, gen uint64) (Info, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		c.ll.Init()
		c.items = map[netip.Addr]*list.Element{}
		c.gen = gen
	}
	if e, ok := c.items[a]; ok {
		c.ll.MoveToFront(e)
		c.hits++
		return e.Value.(*cacheEntry).info, true
	}
	c.misses++
	return Info{}, false
}

func (c *Cache) put(a netip.Addr, info Info, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if e, ok := c.items[a]; ok {
		e.Value.(*cacheEntry).info = info
		return
	}
	c.items[a] = c.ll.PushFront(&cacheEntry{a, info})
	for c.ll.Len() > c.max {
		e := c.ll.Back()
		c.ll.Remove(e)
		delete(c.items, e.Value.(*cacheEntry).a)
	}
}

// Stats are the cache's counters.
type Stats struct {
	Entries int    `json:"entries"`
	Max     int    `json:"max"`
	Hits    uint64 `json:"hits"`
	Misses  uint64 `json:"misses"`
}

// Stats returns the counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Entries: c.ll.Len(), Max: c.max, Hits: c.hits, Misses: c.misses}
}

// String formats an ASN as "AS15169".
func ASNString(n uint32) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("AS%d", n)
}
