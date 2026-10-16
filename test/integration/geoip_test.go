//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"drool/internal/netmon/geoip/mmdbtest"
)

// fakeDBIP stands in for DB-IP's download server: this month's Lite
// databases, ETags and 304s. stall holds a download after its headers (the
// first download in progress); broken serves an unusable database.
type fakeDBIP struct {
	mu        sync.Mutex
	files     map[string][]byte
	downloads int
	stall     chan struct{}
	broken    bool
	srv       *httptest.Server
}

func newFakeDBIP(t *testing.T) *fakeDBIP {
	t.Helper()
	pack := func(b []byte, err error) []byte {
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(b)
		w.Close()
		return buf.Bytes()
	}
	countries := []mmdbtest.Record{{Prefix: "8.8.8.0/24", Data: mmdbtest.Country("US", "United States")}}
	asns := []mmdbtest.Record{{Prefix: "8.8.8.0/24", Data: mmdbtest.ASN(15169, "Google LLC")}}
	for _, n := range testNetworks {
		countries = append(countries, mmdbtest.Record{Prefix: n.prefix + "0/24", Data: mmdbtest.Country(n.cc, n.country)})
		asns = append(asns, mmdbtest.Record{Prefix: n.prefix + "0/24", Data: mmdbtest.ASN(n.asn, n.org)})
	}
	epoch, month := uint64(time.Now().Unix()), time.Now().UTC().Format("2006-01")
	f := &fakeDBIP{files: map[string][]byte{
		"/dbip-country-lite-" + month + ".mmdb.gz": pack(mmdbtest.Build(mmdbtest.Options{DatabaseType: "DBIP-Country-Lite", BuildEpoch: epoch}, countries)),
		"/dbip-asn-lite-" + month + ".mmdb.gz":     pack(mmdbtest.Build(mmdbtest.Options{DatabaseType: "DBIP-ASN-Lite (compat=GeoLite2-ASN)", BuildEpoch: epoch}, asns)),
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body, ok := f.files[r.URL.Path]
		stall, broken := f.stall, f.broken
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if broken {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			gz.Write([]byte("this is not a MaxMind database"))
			gz.Close()
			body = buf.Bytes()
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		f.mu.Lock()
		f.downloads++
		f.mu.Unlock()
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if stall != nil {
			w.(http.Flusher).Flush()
			<-stall
		}
		w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDBIP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads
}

type enrichmentJSON struct {
	State     string `json:"state"`
	Country   bool   `json:"country"`
	ASN       bool   `json:"asn"`
	Downloads struct {
		Enabled   bool                             `json:"enabled"`
		LastError string                           `json:"last_error"`
		Checking  bool                             `json:"checking"`
		Databases []struct{ Kind, Release string } `json:"databases"`
	} `json:"downloads"`
}

func enrichment(t *testing.T) enrichmentJSON {
	t.Helper()
	var v enrichmentJSON
	ctl.must(t, "GET", "/api/v1/network/enrichment", nil, &v)
	return v
}

// TestGeoIPDownloads (UI/UX correction batch §12): a controller without
// databases prepares them itself, in the background, from DB-IP's server
// (a local stand-in here: no test downloads from the internet). Network
// Activity says it is preparing, without a configuration warning; then the
// databases are used and attributed. A restart keeps them without a new
// download; a broken update keeps the databases in use; the operator's own
// files are never touched.
func TestGeoIPDownloads(t *testing.T) {
	f := newFakeDBIP(t)
	stall := make(chan struct{})
	f.stall = stall
	c := freshController(t, "DROOL_NETWORK_GEOIP_DOWNLOAD=true", "DROOL_TEST_GEOIP_URL="+f.srv.URL)
	// The operator's own (unrelated) file in their directory stays as it is.
	mine := filepath.Join(c.dataDir, "geoip", "README.txt")
	os.MkdirAll(filepath.Dir(mine), 0o755)
	os.WriteFile(mine, []byte("my notes"), 0o644)

	// The first download, stalled: preparing.
	waitFor(t, 30*time.Second, "the first download to start", func() bool { return f.count() == 1 })
	st := enrichment(t)
	expect(t, st.State == "preparing" && !st.Country && !st.ASN && st.Downloads.Enabled && st.Downloads.Checking, "while downloading: %+v", st)
	browsed := runE2E(t, c, "geoip", "DROOL_E2E_GEOIP=preparing")
	f.mu.Lock()
	f.stall = nil
	f.mu.Unlock()
	close(stall)
	waitFor(t, 60*time.Second, "the databases to be in use", func() bool { return enrichment(t).State == "available" })
	st = enrichment(t)
	month := time.Now().UTC().Format("2006-01")
	expect(t, st.Country && st.ASN && len(st.Downloads.Databases) == 2 && st.Downloads.Databases[0].Release == month, "available: %+v", st)
	for _, name := range []string{"dbip-country-lite.mmdb", "dbip-asn-lite.mmdb", "state.json"} {
		if _, err := os.Stat(filepath.Join(c.dataDir, "geoip", "downloaded", name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if browsed {
		runE2E(t, c, "geoip", "DROOL_E2E_GEOIP=available")
	}

	// A restart: in use at once, not downloaded again (the next check is
	// due a day after the last).
	before := f.count()
	c.stop()
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.login(); err != nil {
		t.Fatal(err)
	}
	expect(t, enrichment(t).State != "preparing", "after a restart: %+v", enrichment(t))
	waitFor(t, 30*time.Second, "the check after the restart", func() bool {
		st := enrichment(t)
		return st.State == "available" && !st.Downloads.Checking
	})
	time.Sleep(6 * time.Second) // past the first check's start delay
	waitFor(t, 30*time.Second, "the check after the restart to finish", func() bool { return !enrichment(t).Downloads.Checking })
	expect(t, f.count() == before, "downloaded again after a restart: %d → %d", before, f.count())

	// A broken update (its ETag differs, so it is downloaded): refused, the
	// databases in use kept, and said so.
	f.mu.Lock()
	f.broken = true
	f.mu.Unlock()
	ctl.must(t, "POST", "/api/v1/network/enrichment/check", nil, nil)
	waitFor(t, 60*time.Second, "the failed update to be reported", func() bool { return enrichment(t).State == "update_failed" })
	st = enrichment(t)
	expect(t, st.Country && st.ASN, "the databases in use were dropped: %+v", st)
	if browsed {
		runE2E(t, c, "geoip", "DROOL_E2E_GEOIP=failed")
	}
	if b, err := os.ReadFile(mine); err != nil || string(b) != "my notes" {
		t.Fatalf("the operator's file changed: %q %v", b, err)
	}
}
