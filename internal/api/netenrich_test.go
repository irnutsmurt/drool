package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	obs "drool/internal/firewall/observe"
)

// Country and network-owner data say honestly why they're missing
// (dogfooding correction batch): per kind, "off" (lookups turned off),
// "unavailable" (no database of that kind: nothing was looked up) or "on"
// (a database is loaded, so a public address it has no answer for is
// genuinely unknown). Every network view carries the same state; private,
// internal and special addresses are never looked up.
func TestNetworkEnrichmentStates(t *testing.T) {
	e := newFwEnv(t)
	h, _ := e.sshFwHost(t, "geo01")
	now := time.Now().Unix()
	flow := func(peer string) obs.Flow {
		return obs.Flow{Direction: "out", Protocol: "tcp", LocalIP: "10.0.0.1", Peer: peer, Port: 443, Count: 1, FirstSeen: now, LastSeen: now}
	}
	e.report(t, h, obs.SourceSockets, []obs.Flow{flow("142.250.1.1"), flow("8.8.4.4"), flow("10.0.0.9"), flow("203.0.113.5")})
	type state struct{ Country, ASN string }
	views := func() map[string]state {
		t.Helper()
		var sum, host struct {
			State state `json:"enrichment_state"`
		}
		var page struct {
			State state `json:"enrichment_state"`
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		}
		e.c.do("GET", "/api/v1/network/summary", nil).json(t, &sum)
		e.c.do("GET", "/api/v1/hosts/"+h+"/network", nil).json(t, &host)
		e.c.do("GET", "/api/v1/network/flows", nil).json(t, &page)
		var detail struct {
			State state `json:"enrichment_state"`
		}
		e.c.do("GET", fmt.Sprintf("/api/v1/network/flows/%d", page.Items[0].ID), nil).json(t, &detail)
		return map[string]state{"summary": sum.State, "host": host.State, "flows": page.State, "detail": detail.State}
	}
	expectState := func(what string, want state) {
		t.Helper()
		for v, got := range views() {
			if got != want {
				t.Fatalf("%s: the %s view says %+v, want %+v", what, v, got, want)
			}
		}
	}
	geo := func() map[string]netFlow {
		t.Helper()
		out := map[string]netFlow{}
		for _, f := range e.flows(t, "").Items {
			out[f.RemoteIP] = f
		}
		return out
	}
	reload := func() {
		t.Helper()
		if r := e.c.do("POST", "/api/v1/network/enrichment/reload", nil); r.status != 200 {
			t.Fatalf("reload: %d %s", r.status, r.body)
		}
	}
	setLookups := func(on bool) {
		t.Helper()
		if r := e.c.do("PUT", "/api/v1/network/settings", map[string]any{"collection": "all", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": on}); r.status != 200 {
			t.Fatalf("settings: %d %s", r.status, r.body)
		}
	}
	notLookedUp := func(what string) {
		t.Helper()
		for ip, f := range geo() {
			if f.Geo != nil {
				t.Fatalf("%s: %s has geo data %+v", what, ip, f.Geo)
			}
		}
	}

	// Lookups on (the default), no database: nothing could be looked up.
	expectState("no database", state{"unavailable", "unavailable"})
	notLookedUp("no database")

	// Both databases, lookups turned off: off, not "unavailable".
	writeGeoDBs(t, e.h.geoDir)
	reload()
	setLookups(false)
	expectState("lookups off", state{"off", "off"})
	notLookedUp("lookups off")

	// Both, on: a known address has both; one the databases don't have is
	// genuinely unknown (state on, no data); private and documentation
	// addresses are never looked up.
	setLookups(true)
	expectState("both databases", state{"on", "on"})
	g := geo()
	if f := g["142.250.1.1"]; f.Geo == nil || f.Geo.CountryCode != "US" || f.Geo.ASN != 15169 {
		t.Fatalf("a known address: %+v", f.Geo)
	}
	if f := g["8.8.4.4"]; f.Scope != "public" || f.Geo != nil {
		t.Fatalf("a public address the databases don't have: %+v", f)
	}
	if f := g["10.0.0.9"]; f.Scope != "internal" || f.Geo != nil {
		t.Fatalf("a private address: %+v", f)
	}
	if f := g["203.0.113.5"]; f.Scope != "special" || f.Geo != nil {
		t.Fatalf("a documentation address (in the test country database): %+v", f)
	}

	// Country only: countries shown, network owners not looked up.
	if err := os.Remove(filepath.Join(e.h.geoDir, "GeoLite2-ASN.mmdb")); err != nil {
		t.Fatal(err)
	}
	reload()
	expectState("country only", state{"on", "unavailable"})
	if f := geo()["142.250.1.1"]; f.Geo == nil || f.Geo.CountryCode != "US" || f.Geo.ASN != 0 {
		t.Fatalf("country only: %+v", f.Geo)
	}

	// ASN only: the other way round.
	if err := os.Remove(filepath.Join(e.h.geoDir, "GeoLite2-Country.mmdb")); err != nil {
		t.Fatal(err)
	}
	writeGeoDBs(t, e.h.geoDir)
	if err := os.Remove(filepath.Join(e.h.geoDir, "GeoLite2-Country.mmdb")); err != nil {
		t.Fatal(err)
	}
	reload()
	expectState("ASN only", state{"unavailable", "on"})
	if f := geo()["142.250.1.1"]; f.Geo == nil || f.Geo.CountryCode != "" || f.Geo.ASN != 15169 || f.Geo.Org != "Google LLC" {
		t.Fatalf("ASN only: %+v", f.Geo)
	}

	// A half-written replacement keeps the copy loaded before (its data is
	// still shown, so still "on")...
	if err := os.WriteFile(filepath.Join(e.h.geoDir, "GeoLite2-ASN.mmdb"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	reload()
	expectState("a broken replacement", state{"unavailable", "on"})
	// ...but a database that never loaded is no database.
	if err := os.Remove(filepath.Join(e.h.geoDir, "GeoLite2-ASN.mmdb")); err != nil {
		t.Fatal(err)
	}
	reload()
	if err := os.WriteFile(filepath.Join(e.h.geoDir, "other-asn.mmdb"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	reload()
	expectState("only an unreadable database", state{"unavailable", "unavailable"})
	notLookedUp("only an unreadable database")
}
