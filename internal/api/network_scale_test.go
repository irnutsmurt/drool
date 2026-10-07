package api

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	obs "drool/internal/firewall/observe"
)

// TestNetworkScale: 200 hosts with 500 flows each (100 000 rows). The
// fleet views must stay interactive: pages, filters that need the
// classifier, the summary and the topology. Run with DROOL_SCALE_TEST=1
// (slow: it ingests through the real path).
func TestNetworkScale(t *testing.T) {
	if os.Getenv("DROOL_SCALE_TEST") == "" {
		t.Skip("set DROOL_SCALE_TEST=1")
	}
	e := newFwEnv(t)
	ctx := context.Background()
	writeGeoDBs(t, e.h.geoDir)
	e.h.net.Enricher().Refresh()
	now := time.Now().Unix()
	start := time.Now()
	for h := 0; h < 200; h++ {
		id := fmt.Sprintf("host_scale%03d", h)
		if _, err := e.h.st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'x', 'ready', 0, 0)`,
			id, fmt.Sprintf("node-%03d", h)); err != nil {
			t.Fatal(err)
		}
		e.h.st.W.UpsertHostAddress(ctx, upsertAddr(id, fmt.Sprintf("10.50.%d.%d", h/250, h%250+1)))
		var fl []obs.Flow
		for i := 0; i < 500; i++ {
			peer := fmt.Sprintf("142.250.%d.%d", i/250, i%250+1)
			switch i % 5 {
			case 1:
				peer = fmt.Sprintf("185.10.%d.%d", h%250, i%250+1)
			case 2:
				peer = fmt.Sprintf("2a01:4f8:%x::%x", h, i+1)
			case 3:
				peer = fmt.Sprintf("10.50.%d.%d", (h+1)%200/250, (h+1)%200%250+1)
			}
			fl = append(fl, obs.Flow{Direction: "out", Protocol: "tcp", LocalIP: "10.50.0.1", Peer: peer, Port: 443 + i%7, Count: int64(1 + i%40),
				FirstSeen: now - int64(i*600), LastSeen: now - int64(i)})
		}
		e.report(t, id, obs.SourceConntrack, fl)
	}
	t.Logf("ingested 200 × 500 flows in %v", time.Since(start).Round(time.Millisecond))
	for _, q := range []string{"", "scope=external", "policy=unclassified", "country=NL", "asn=15169", "q=node-150", "new=fleet&since=24h", "host=host_scale150", "port=445&sort=count"} {
		t0 := time.Now()
		p := e.flows(t, q+"&limit=50")
		t.Logf("flows?%-24s %3d items in %v (total %v)", q, len(p.Items), time.Since(t0).Round(time.Millisecond), func() any {
			if p.Total == nil {
				return "n/a"
			}
			return *p.Total
		}())
	}
	for _, path := range []string{"/api/v1/network/summary", "/api/v1/network/filters", "/api/v1/network/topology", "/api/v1/hosts/host_scale150/network"} {
		t0 := time.Now()
		if r := e.c.do("GET", path, nil); r.status != 200 {
			t.Fatalf("%s: %d %s", path, r.status, r.body)
		}
		t.Logf("%-40s %v", path, time.Since(t0).Round(time.Millisecond))
	}
}
