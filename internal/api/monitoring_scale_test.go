package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/monitor"
	"drool/internal/monitor/monsvc"
)

// TestMonitoringScale measures metric storage and query cost for 5, 50 and
// 200 hosts at steady state: a full retention period of history in every
// tier (26 h of minutes, 8 days of five minutes, 30 days of hours) for 33
// series per host, written through the service's own encoder. Then one
// minute of live ingestion for every host, the views, a rollup pass and a
// prune. Run with DROOL_SCALE_TEST=1.
func TestMonitoringScale(t *testing.T) {
	if os.Getenv("DROOL_SCALE_TEST") == "" {
		t.Skip("set DROOL_SCALE_TEST=1")
	}
	keys := []string{"cpu", "cpu.iowait", "mem", "swap", "load1", "load5", "load15", "psi.cpu", "psi.mem", "psi.io",
		"fs:/", "fs:/boot", "fs:/data", "inodes:/", "inodes:/boot", "inodes:/data",
		"net.rx:eth0", "net.tx:eth0", "net.err:eth0", "net.drop:eth0", "net.rx:eth1", "net.tx:eth1", "net.err:eth1", "net.drop:eth1",
		"disk.r:sda", "disk.w:sda", "disk.busy:sda", "disk.r:nvme0n1", "disk.w:nvme0n1", "disk.busy:nvme0n1",
		"temp:coretemp/Package id 0", "temp:coretemp/Core 0", "temp:nvme:nvme0n1/Composite"}
	for _, n := range []int{5, 50, 200} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			e := newFwEnv(t)
			m := e.h.mon
			ctx := context.Background()
			waves := map[string]monsvc.TestWave{}
			for i, k := range keys {
				waves[k] = monsvc.TestWave{Base: float64(10 + i), Amp: 5, PeriodH: 24}
			}
			fixture := map[string]any{}
			var ids []string
			for h := 0; h < n; h++ {
				name := fmt.Sprintf("node-%03d", h)
				id := fmt.Sprintf("host_mscale%03d", h)
				if _, err := e.h.st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'x', 'ready', 0, 0)`,
					id, name); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
				st := stateAt(time.Now(), monitor.FS{Mount: "/", TotalBytes: 100 << 30, UsedBytes: 40 << 30, AvailBytes: 60 << 30, UsedPercent: 40})
				fixture[name] = map[string]any{"series": waves, "state": st}
			}
			b, _ := json.Marshal(map[string]any{"hosts": fixture})
			path := filepath.Join(t.TempDir(), "fixture.json")
			os.WriteFile(path, b, 0o644)
			t0 := time.Now()
			if err := m.LoadTestFile(ctx, path); err != nil {
				t.Fatal(err)
			}
			fill := time.Since(t0)
			size := func() (int64, map[int]int) {
				var pages, psize int64
				e.h.st.Reader.QueryRow(`PRAGMA page_count`).Scan(&pages)
				e.h.st.Reader.QueryRow(`PRAGMA page_size`).Scan(&psize)
				rows := map[int]int{}
				for tier := 0; tier <= 2; tier++ {
					var c int
					e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM mon_points WHERE tier = ?`, tier).Scan(&c)
					rows[tier] = c
				}
				return pages * psize, rows
			}
			e.h.st.Writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
			bytes, rows := size()
			var dataBytes int64
			e.h.st.Reader.QueryRow(`SELECT SUM(LENGTH(data)) FROM mon_points`).Scan(&dataBytes)
			t.Logf("%3d hosts: %d/%d/%d points (raw/5m/1h) written in %v; database %.1f MB (%.2f MB per host, %d payload bytes per point)",
				n, rows[0], rows[1], rows[2], fill.Round(time.Millisecond), float64(bytes)/1e6, float64(bytes)/1e6/float64(n), dataBytes/int64(rows[0]+rows[1]+rows[2]))
			// One live minute for every host (what the fleet sends each minute).
			t0 = time.Now()
			for _, id := range ids {
				p := monitor.Point{T: time.Now().Unix() / 60 * 60, S: 60, N: 6, V: map[string][2]float64{}}
				for _, k := range keys {
					p.V[k] = [2]float64{20, 30}
				}
				if err := m.IngestPoints(ctx, id, []monitor.Point{p}, nil); err != nil {
					t.Fatal(err)
				}
			}
			live := time.Since(t0)
			t.Logf("%3d hosts: one minute of live points ingested in %v (%.2f ms per host)", n, live.Round(time.Millisecond), float64(live.Microseconds())/1000/float64(n))
			for _, rng := range []string{"1h", "24h", "7d", "30d"} {
				t0 = time.Now()
				var h monsvc.History
				e.c.do("GET", "/api/v1/hosts/"+ids[n/2]+"/monitoring/history?range="+rng+"&series=cpu&series=mem&series=fs:&series=net.rx:", nil).json(t, &h)
				pts := 0
				for _, s := range h.Series {
					pts += len(s.Points)
				}
				t.Logf("%3d hosts: history %-3s %d series, %d points in %v", n, rng, len(h.Series), pts, time.Since(t0).Round(time.Millisecond))
			}
			t0 = time.Now()
			var f monsvc.FleetSummary
			e.c.do("GET", "/api/v1/monitoring/summary?limit=100", nil).json(t, &f)
			t.Logf("%3d hosts: fleet summary (%d rows) in %v", n, len(f.Rows), time.Since(t0).Round(time.Millisecond))
			t0 = time.Now()
			e.c.do("GET", "/api/v1/hosts/"+ids[0]+"/monitoring", nil)
			t.Logf("%3d hosts: host overview in %v", n, time.Since(t0).Round(time.Millisecond))
			t0 = time.Now()
			if err := m.Rollups(ctx); err != nil {
				t.Fatal(err)
			}
			t.Logf("%3d hosts: rollup pass in %v", n, time.Since(t0).Round(time.Millisecond))
			t0 = time.Now()
			r := m.Prune(ctx)
			t.Logf("%3d hosts: prune pass (%d points removed) in %v", n, r.Points, time.Since(t0).Round(time.Millisecond))
			if f.Total != n || !strings.HasPrefix(f.Rows[0].Name, "node-") {
				t.Fatalf("fleet: %+v", f.Total)
			}
		})
	}
}
