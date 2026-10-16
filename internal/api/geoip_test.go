package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	"drool/internal/netmon/geoip/mmdbtest"
)

// geoServer is a stand-in for DB-IP's download server publishing this
// month's Lite databases.
func geoServer(t *testing.T) *httptest.Server {
	t.Helper()
	pack := func(opt mmdbtest.Options, recs []mmdbtest.Record) []byte {
		b, err := mmdbtest.Build(opt, recs)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(b)
		w.Close()
		return buf.Bytes()
	}
	epoch := uint64(time.Now().Unix())
	month := time.Now().UTC().Format("2006-01")
	files := map[string][]byte{
		"/dbip-country-lite-" + month + ".mmdb.gz": pack(mmdbtest.Options{DatabaseType: "DBIP-Country-Lite", BuildEpoch: epoch},
			[]mmdbtest.Record{{Prefix: "8.8.8.0/24", Data: mmdbtest.Country("US", "United States")}}),
		"/dbip-asn-lite-" + month + ".mmdb.gz": pack(mmdbtest.Options{DatabaseType: "DBIP-ASN-Lite", BuildEpoch: epoch},
			[]mmdbtest.Record{{Prefix: "8.8.8.0/24", Data: mmdbtest.ASN(15169, "Google LLC")}}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type enrichStatusJSON struct {
	State   string `json:"state"`
	Country bool   `json:"country"`
	ASN     bool   `json:"asn"`
	Files   []struct {
		Name       string   `json:"name"`
		Downloaded bool     `json:"downloaded"`
		Used       []string `json:"used"`
	} `json:"files"`
	Downloads *struct {
		Enabled     bool                             `json:"enabled"`
		Disabled    string                           `json:"disabled"`
		Provider    string                           `json:"provider"`
		Attribution string                           `json:"attribution"`
		LastSuccess string                           `json:"last_success"`
		Databases   []struct{ Kind, Release string } `json:"databases"`
	} `json:"downloads"`
}

// TestGeoIPDownloads (UI/UX batch §12): enrichment prepares itself without
// setup, says so while it does, can be checked now and turned off — and
// a client that doesn't know the setting doesn't turn it off.
func TestGeoIPDownloads(t *testing.T) {
	srv := geoServer(t)
	h := newHarnessWith(t, harnessOpts{geoipURL: srv.URL})
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("operator", auth.RoleOperator)
	c := h.login("admin")
	status := func() enrichStatusJSON {
		t.Helper()
		var v struct {
			GeoIPDownloads bool             `json:"geoip_downloads"`
			Enrichment     enrichStatusJSON `json:"enrichment_status"`
		}
		c.do("GET", "/api/v1/network/settings", nil).json(t, &v)
		return v.Enrichment
	}
	// Before the first download: being prepared, not "not configured".
	st := status()
	if st.State != "preparing" || st.Downloads == nil || !st.Downloads.Enabled || st.Downloads.Provider != "DB-IP Lite" || st.Downloads.Attribution != "IP Geolocation by DB-IP" {
		t.Fatalf("before: %+v %+v", st, st.Downloads)
	}
	var page struct {
		State struct{ Country, ASN string } `json:"enrichment_state"`
	}
	c.do("GET", "/api/v1/network/flows", nil).json(t, &page)
	if page.State.Country != "preparing" || page.State.ASN != "preparing" {
		t.Fatalf("flows while preparing: %+v", page.State)
	}
	// The first download: available, downloaded, used.
	if err := h.net.CheckGeoIPNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = status()
	if st.State != "available" || !st.Country || !st.ASN || len(st.Files) != 2 || len(st.Downloads.Databases) != 2 || st.Downloads.LastSuccess == "" {
		t.Fatalf("after: %+v", st)
	}
	for _, f := range st.Files {
		if !f.Downloaded || len(f.Used) != 1 {
			t.Fatalf("file: %+v", f)
		}
	}
	// Check now: for those who may change the settings; audited.
	if r := h.login("operator").do("POST", "/api/v1/network/enrichment/check", nil); r.status != http.StatusForbidden {
		t.Fatalf("operator: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/network/enrichment/check", nil); r.status != http.StatusAccepted {
		t.Fatalf("check now: %d %s", r.status, r.body)
	}
	var audits listResponse[auditJSON]
	c.do("GET", "/api/v1/audit?action=network.enrichment.check", nil).json(t, &audits)
	if len(audits.Items) != 1 {
		t.Fatalf("Check now not audited: %+v", audits)
	}
	// Turned off: no Check now; what was downloaded is still used.
	off := map[string]any{"collection": "all", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": true, "geoip_downloads": false}
	if r := c.do("PUT", "/api/v1/network/settings", off); r.status != 200 {
		t.Fatalf("settings: %d %s", r.status, r.body)
	}
	st = status()
	if st.State != "available" || st.Downloads.Enabled || st.Downloads.Disabled != "turned off in these settings" {
		t.Fatalf("off: %+v %+v", st, st.Downloads)
	}
	if r := c.do("POST", "/api/v1/network/enrichment/check", nil); r.status != http.StatusConflict || r.problemCode(t) != "downloads_off" ||
		!strings.Contains(string(r.body), "turned off in these settings") {
		t.Fatalf("check while off: %d %s", r.status, r.body)
	}
	// A client that doesn't send geoip_downloads keeps it as it is.
	legacy := map[string]any{"collection": "all", "retention_days": 31, "max_flows_per_host": 5000, "enrichment": true}
	var after struct {
		GeoIPDownloads bool `json:"geoip_downloads"`
		RetentionDays  int  `json:"retention_days"`
	}
	c.do("PUT", "/api/v1/network/settings", legacy).json(t, &after)
	if after.GeoIPDownloads || after.RetentionDays != 31 {
		t.Fatalf("legacy save: %+v", after)
	}
	// Lookups off: off, whatever is installed.
	lookupsOff := map[string]any{"collection": "all", "retention_days": 31, "max_flows_per_host": 5000, "enrichment": false, "geoip_downloads": true}
	c.do("PUT", "/api/v1/network/settings", lookupsOff)
	if st = status(); st.State != "off" || st.Downloads.Enabled {
		t.Fatalf("lookups off: %+v %+v", st, st.Downloads)
	}
}

// TestGeoIPWithoutDownloads: a controller that downloads nothing (the
// configuration turned it off) says the databases aren't configured.
func TestGeoIPWithoutDownloads(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	var v struct {
		Enrichment enrichStatusJSON `json:"enrichment_status"`
	}
	c.do("GET", "/api/v1/network/settings", nil).json(t, &v)
	if v.Enrichment.State != "not_configured" || v.Enrichment.Downloads != nil {
		t.Fatalf("status: %+v", v.Enrichment)
	}
	if r := c.do("POST", "/api/v1/network/enrichment/check", nil); r.status != http.StatusConflict {
		t.Fatalf("check: %d %s", r.status, r.body)
	}
}
