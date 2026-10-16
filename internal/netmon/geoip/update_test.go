package geoip_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/netmon/geoip"
	"drool/internal/netmon/geoip/mmdbtest"
)

// provider is a fake download server: DB-IP's file names, ETags, 304s
// and 404s for months not published yet.
type provider struct {
	mu      sync.Mutex
	files   map[string][]byte // name → body (gzip)
	hits    map[string]int    // name → full downloads (200)
	asks    map[string]int    // name → requests of any kind
	fail    int               // answer 500 to this many requests
	hold    chan struct{}     // when set, requests wait for it to close
	in      chan string       // requests arriving (when set)
	chunked map[string]bool   // names sent without a Content-Length
	srv     *httptest.Server
}

func newProvider(t *testing.T) *provider {
	p := &provider{files: map[string][]byte{}, hits: map[string]int{}, asks: map[string]int{}, chunked: map[string]bool{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		p.mu.Lock()
		p.asks[name]++
		hold, in := p.hold, p.in
		fail := p.fail > 0
		if fail {
			p.fail--
		}
		body, ok := p.files[name]
		chunked := p.chunked[name]
		p.mu.Unlock()
		if in != nil {
			in <- name
		}
		if hold != nil {
			<-hold
		}
		if fail {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		etag := fmt.Sprintf(`"%x-%d"`, len(body), body[len(body)-1])
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		p.mu.Lock()
		p.hits[name]++
		p.mu.Unlock()
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", "Thu, 01 Oct 2026 06:29:26 GMT")
		if chunked {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush() // no Content-Length: streamed
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.Write(body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) put(name string, body []byte) {
	p.mu.Lock()
	p.files[name] = body
	p.mu.Unlock()
}

// set changes the provider's behaviour under its lock.
func (p *provider) set(f func(p *provider)) {
	p.mu.Lock()
	f(p)
	p.mu.Unlock()
}

func (p *provider) count(m map[string]int, name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return m[name]
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func countryAt(t *testing.T, epoch uint64, cc string) []byte {
	t.Helper()
	b, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "DBIP-Country-Lite", BuildEpoch: epoch}, []mmdbtest.Record{
		{Prefix: "8.8.8.0/24", Data: mmdbtest.Country(cc, "Somewhere")},
		{Prefix: "185.10.0.0/16", Data: mmdbtest.Country("NL", "Netherlands")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asnAt(t *testing.T, epoch uint64) []byte {
	t.Helper()
	b, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "DBIP-ASN-Lite (compat=GeoLite2-ASN)", BuildEpoch: epoch}, []mmdbtest.Record{
		{Prefix: "8.8.8.0/24", Data: mmdbtest.ASN(15169, "Google LLC")},
		{Prefix: "185.10.0.0/16", Data: mmdbtest.ASN(64512, "Example Hosting B.V.")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	oct      = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	octBuild = uint64(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix())
	novBuild = uint64(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix())
)

// publish puts a month's two databases on the provider.
func (p *provider) publish(t *testing.T, month string, epoch uint64, cc string) {
	p.put("dbip-country-lite-"+month+".mmdb.gz", gz(t, countryAt(t, epoch, cc)))
	p.put("dbip-asn-lite-"+month+".mmdb.gz", gz(t, asnAt(t, epoch)))
}

type setup struct {
	yours, dir string
	e          *geoip.Enricher
	u          *geoip.Updater
	now        atomic.Pointer[time.Time]
	allowed    atomic.Bool
}

// newSetup is a controller's enricher and updater over an operator
// directory and DROOL's download directory; start again with restart.
func newSetup(t *testing.T, p *provider) *setup {
	t.Helper()
	root := t.TempDir()
	s := &setup{yours: filepath.Join(root, "geoip"), dir: filepath.Join(root, "geoip", "downloaded")}
	os.MkdirAll(s.yours, 0o755)
	n := oct
	s.now.Store(&n)
	s.allowed.Store(true)
	s.restart(t, p)
	return s
}

func (s *setup) restart(t *testing.T, p *provider) {
	t.Helper()
	s.e = geoip.NewEnricher(s.yours, 100)
	s.e.SetDownloadDir(s.dir)
	s.e.Refresh()
	s.u = geoip.NewUpdater(geoip.UpdaterConfig{
		Dir: s.dir, BaseURL: p.srv.URL, Now: func() time.Time { return *s.now.Load() }, RetryWait: time.Millisecond,
		Allowed: func(context.Context) (bool, string) {
			if s.allowed.Load() {
				return true, ""
			}
			return false, "turned off in the settings"
		},
		MaxDownload: 1 << 20, MaxDatabase: 1 << 20,
	}, s.e)
}

func (s *setup) at(t time.Time) { s.now.Store(&t) }

func lookup(e *geoip.Enricher, ip string) geoip.Info { return e.Lookup(netip.MustParseAddr(ip)) }

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(left) > 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestUpdaterDownloadsWhatIsMissingThenOnlyChecks(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	if c, a := s.e.Kinds(); c || a {
		t.Fatal("databases before any download")
	}
	if !s.u.Pending(context.Background(), geoip.KindCountry) {
		t.Fatal("the first download should be pending (preparing)")
	}
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" || got.ASN != 15169 {
		t.Fatalf("lookup after download: %+v", got)
	}
	st := s.u.Status(context.Background())
	if len(st.Databases) != 2 || st.Databases[0].Release != "2026-10" || st.Databases[0].BuildDate != "2026-10-01" || st.LastSuccess == "" || st.LastError != "" {
		t.Fatalf("status: %+v", st)
	}
	if st.Provider != "DB-IP Lite" || st.Attribution != "IP Geolocation by DB-IP" || st.License != "CC BY 4.0" {
		t.Fatalf("provider: %+v", st)
	}
	for _, f := range s.e.Status().Files {
		if !f.Downloaded || len(f.Used) != 1 {
			t.Fatalf("file %+v", f)
		}
	}
	noTempFiles(t, s.dir)
	// A day later: the same release, asked for conditionally (304), not
	// downloaded again.
	s.at(oct.Add(24 * time.Hour))
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"country", "asn"} {
		name := "dbip-" + k + "-lite-2026-10.mmdb.gz"
		if p.count(p.hits, name) != 1 || p.count(p.asks, name) != 2 {
			t.Fatalf("%s: %d downloads, %d requests", name, p.count(p.hits, name), p.count(p.asks, name))
		}
	}
	if s.u.Pending(context.Background(), geoip.KindCountry) {
		t.Fatal("pending after a download")
	}
}

func TestUpdaterLeavesTheOperatorsDatabasesAlone(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	// The operator's own (older) country database, under a name of theirs.
	mine := filepath.Join(s.yours, "my-country.mmdb")
	os.WriteFile(mine, countryAt(t, octBuild-86400*90, "CA"), 0o644)
	before, _ := os.Stat(mine)
	s.e.Refresh()
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.count(p.asks, "dbip-country-lite-2026-10.mmdb.gz") != 0 {
		t.Fatal("downloaded a kind the operator provides")
	}
	if p.count(p.hits, "dbip-asn-lite-2026-10.mmdb.gz") != 1 {
		t.Fatal("the missing ASN database wasn't downloaded")
	}
	after, _ := os.Stat(mine)
	if !after.ModTime().Equal(before.ModTime()) || !bytes.Equal(fileBytes(t, mine), countryAt(t, octBuild-86400*90, "CA")) {
		t.Fatal("the operator's file changed")
	}
	// Even a newer downloaded country database doesn't replace theirs.
	os.WriteFile(filepath.Join(s.dir, "dbip-country-lite.mmdb"), countryAt(t, novBuild, "US"), 0o644)
	s.e.Refresh()
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "CA" || got.ASN != 15169 {
		t.Fatalf("lookup: %+v (the operator's country, the downloaded ASN)", got)
	}
	if st := s.u.Status(context.Background()); len(st.Yours) != 1 || st.Yours[0] != "country" {
		t.Fatalf("status yours: %+v", st.Yours)
	}
}

func TestUpdaterRejectsBadDownloadsAndKeepsTheDatabaseInUse(t *testing.T) {
	big := make([]byte, 2<<20)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range big {
		big[i] = byte(r.Uint32()) // doesn't compress
	}
	for _, tc := range []struct {
		name    string
		body    func(t *testing.T) []byte
		want    string
		chunked bool
	}{
		{"not gzip", func(*testing.T) []byte { return []byte("<html>maintenance</html>") }, "not a gzip file", false},
		{"not a database", func(t *testing.T) []byte { return gz(t, []byte("not a MaxMind DB at all")) }, "not usable", false},
		{"the wrong data", func(t *testing.T) []byte { return gz(t, asnAt(t, novBuild)) }, "has no country data", false},
		{"truncated", func(t *testing.T) []byte { b := gz(t, countryAt(t, novBuild, "US")); return b[:len(b)-12] }, "incomplete or damaged", false},
		{"too large to download", func(t *testing.T) []byte { return big }, "download is too large", false},
		{"too large, without a length", func(t *testing.T) []byte { return gz(t, big) }, "download is larger than", true},
		{"too large unpacked", func(t *testing.T) []byte { return gz(t, make([]byte, 3<<20)) }, "database is larger than", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProvider(t)
			p.publish(t, "2026-10", octBuild, "US")
			s := newSetup(t, p)
			if err := s.u.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.dir, "dbip-country-lite.mmdb")
			good := fileBytes(t, path)
			// November's country file is bad; its ASN file is fine.
			p.put("dbip-country-lite-2026-11.mmdb.gz", tc.body(t))
			p.set(func(p *provider) { p.chunked["dbip-country-lite-2026-11.mmdb.gz"] = tc.chunked })
			p.put("dbip-asn-lite-2026-11.mmdb.gz", gz(t, asnAt(t, novBuild)))
			s.at(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
			err := s.u.Check(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
			if !bytes.Equal(fileBytes(t, path), good) {
				t.Fatal("the country database in use was replaced")
			}
			if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" || got.ASN != 15169 {
				t.Fatalf("lookup: %+v", got)
			}
			st := s.u.Status(context.Background())
			if !strings.Contains(st.LastError, "country database") || st.Databases[0].Release != "2026-10" || st.Databases[1].Release != "2026-11" {
				t.Fatalf("status: %+v", st)
			}
			noTempFiles(t, s.dir)
		})
	}
}

func TestUpdaterKeepsWorkingDatabasesWhenTheProviderIsGone(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	success := s.u.Status(context.Background()).LastSuccess
	p.srv.Close()
	s.at(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
	err := s.u.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "can't reach") {
		t.Fatalf("error: %v", err)
	}
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" {
		t.Fatalf("lookup: %+v", got)
	}
	st := s.u.Status(context.Background())
	if st.LastSuccess != success || st.LastError == "" || len(st.Databases) != 2 {
		t.Fatalf("status: %+v", st)
	}
}

func TestUpdaterOfflineFromTheStart(t *testing.T) {
	p := newProvider(t)
	p.srv.Close()
	s := newSetup(t, p)
	start := time.Now()
	if err := s.u.Check(context.Background()); err == nil {
		t.Fatal("no error offline")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("an offline check took too long")
	}
	if c, a := s.e.Kinds(); c || a {
		t.Fatal("databases appeared")
	}
	// Not "preparing" any more: the page says it's unavailable for now.
	if s.u.Pending(context.Background(), geoip.KindCountry) {
		t.Fatal("still pending after a failed attempt")
	}
	noTempFiles(t, s.dir)
}

func TestUpdaterRetriesATransientFailure(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	p.set(func(p *provider) { p.fail = 2 }) // two 500s, then it answers
	s := newSetup(t, p)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" || got.ASN != 15169 {
		t.Fatalf("lookup: %+v", got)
	}
}

func TestUpdaterSurvivesARestart(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.restart(t, p)
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" || got.ASN != 15169 {
		t.Fatalf("lookup after a restart: %+v", got)
	}
	if st := s.u.Status(context.Background()); len(st.Databases) != 2 || st.LastSuccess == "" {
		t.Fatalf("status after a restart: %+v", st)
	}
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := p.count(p.hits, "dbip-country-lite-2026-10.mmdb.gz"); n != 1 {
		t.Fatalf("downloaded %d times across a restart", n)
	}
}

func TestUpdaterUsesLastMonthUntilTheNewOneIsPublished(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	s.at(time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)) // before DB-IP publishes November's
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.u.Status(context.Background()); st.Databases[0].Release != "2026-10" {
		t.Fatalf("release %+v", st.Databases)
	}
	// Still early: November isn't out, October is what there is.
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := p.count(p.hits, "dbip-country-lite-2026-10.mmdb.gz"); n != 1 {
		t.Fatalf("October downloaded %d times", n)
	}
	p.publish(t, "2026-11", novBuild, "GB")
	s.at(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "GB" {
		t.Fatalf("November's data not in use: %+v", got)
	}
}

func TestUpdaterNeverInstallsAnOlderBuild(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", novBuild, "US")
	s := newSetup(t, p)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.publish(t, "2026-11", octBuild, "GB") // a release built earlier than the one installed
	s.at(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err := s.u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "older than the database in use") {
		t.Fatalf("error: %v", err)
	}
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" {
		t.Fatalf("lookup: %+v", got)
	}
}

func TestUpdaterRunsOneCheckAtATime(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	hold, in := make(chan struct{}), make(chan string, 10)
	p.set(func(p *provider) { p.hold, p.in = hold, in })
	s := newSetup(t, p)
	done := make(chan error)
	go func() { done <- s.u.Check(context.Background()) }()
	<-in // the first check is downloading
	if !s.u.Status(context.Background()).Checking {
		t.Fatal("status doesn't say it's checking")
	}
	second := make(chan error, 1)
	go func() { second <- s.u.Check(context.Background()) }()
	select {
	case err := <-second:
		if !errors.Is(err, geoip.ErrChecking) {
			t.Fatalf("a second check: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(hold)
		t.Fatal("a second check waited for the first instead of refusing")
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"country", "asn"} {
		if n := p.count(p.asks, "dbip-"+k+"-lite-2026-10.mmdb.gz"); n != 1 {
			t.Fatalf("%s requested %d times", k, n)
		}
	}
}

func TestUpdaterDoesNothingWhenTurnedOff(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	s.allowed.Store(false)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := p.count(p.asks, "dbip-country-lite-2026-10.mmdb.gz"); n != 0 {
		t.Fatalf("%d requests while turned off", n)
	}
	st := s.u.Status(context.Background())
	if st.Enabled || st.Disabled != "turned off in the settings" || st.NextCheck != "" {
		t.Fatalf("status: %+v", st)
	}
	if s.u.Pending(context.Background(), geoip.KindCountry) {
		t.Fatal("pending while turned off")
	}
}

func TestUpdaterReplacesADamagedDownload(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, "dbip-country-lite.mmdb")
	os.WriteFile(path, []byte("damaged"), 0o644)
	s.e.Refresh()
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := p.count(p.hits, "dbip-country-lite-2026-10.mmdb.gz"); n != 2 {
		t.Fatalf("downloaded %d times, want it again after the damage", n)
	}
	s.e.Refresh()
	for _, f := range s.e.Status().Files {
		if f.Error != "" {
			t.Fatalf("file still damaged: %+v", f)
		}
	}
}

func TestUpdaterRestartChecksOnlyWhenDue(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	if err := s.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Restarted an hour later: in use at once, and the next check is due
	// a day after the last one, not now.
	s.at(oct.Add(time.Hour))
	s.restart(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.u.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for s.u.Status(ctx).NextCheck == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	next, err := time.Parse(time.RFC3339, s.u.Status(ctx).NextCheck)
	if err != nil || !next.Equal(oct.Add(24*time.Hour)) {
		t.Fatalf("next check %v %v, want a day after the last", next, err)
	}
	if got := lookup(s.e, "8.8.8.8"); got.CountryCode != "US" {
		t.Fatalf("lookup after the restart: %+v", got)
	}
	// A temporary file a crash left behind is cleaned up at startup.
	os.WriteFile(filepath.Join(s.dir, ".download-123.tmp"), []byte("partial"), 0o600)
	s.restart(t, p)
	noTempFiles(t, s.dir)
}

func TestUpdaterRunChecksOnTrigger(t *testing.T) {
	p := newProvider(t)
	p.publish(t, "2026-10", octBuild, "US")
	s := newSetup(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.u = geoip.NewUpdater(geoip.UpdaterConfig{Dir: s.dir, BaseURL: p.srv.URL, StartDelay: time.Hour, RetryWait: time.Millisecond,
		Now: func() time.Time { return oct }}, s.e)
	go s.u.Run(ctx)
	s.u.Trigger()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, a := s.e.Kinds(); c && a {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Trigger didn't start a check")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
