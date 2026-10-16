package geoip

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Automatic databases (UI/UX correction batch §12). Country and network
// owner work without setup: DROOL downloads DB-IP Lite Country and ASN
// (CC BY 4.0, monthly releases, no account needed) into a directory of its
// own, checks daily for a newer release, validates each download and swaps
// it in atomically. The operator's own databases are never touched and win
// for the kinds they provide: DROOL downloads only what is missing. Only
// the database files are downloaded; lookups stay local.

// The provider of the databases DROOL downloads, and the attribution its
// licence asks for wherever the data is shown.
const (
	ProviderName    = "DB-IP Lite"
	ProviderURL     = "https://db-ip.com"
	ProviderLicense = "CC BY 4.0"
	Attribution     = "IP Geolocation by DB-IP"
	// DefaultDownloadURL is where DB-IP publishes its free databases, as
	// dbip-{country,asn}-lite-YYYY-MM.mmdb.gz on the first of each month.
	DefaultDownloadURL = "https://download.db-ip.com/free/"
)

// Limits on one download: the compressed file (DB-IP's are ~5 MB) and the
// database it unpacks to (~10 MB).
const (
	MaxDownloadSize = 64 << 20
	MaxDatabaseSize = 256 << 20
)

const (
	checkEvery   = 24 * time.Hour
	retryMissing = time.Hour // nothing downloaded yet and the last try failed
	attempts     = 3         // per database and check
)

// ErrChecking is returned when a check is already running.
var ErrChecking = errors.New("already checking for new databases")

var errNotPublished = errors.New("not published yet")

// UpdaterConfig configures automatic downloads.
type UpdaterConfig struct {
	// Dir holds DROOL's own downloads (<data_dir>/geoip/downloaded), never
	// the operator's files.
	Dir string
	// BaseURL replaces DefaultDownloadURL (tests).
	BaseURL string
	// Allowed reports whether downloading is allowed now and, if not, why
	// (turned off in the configuration or the settings, lookups off).
	Allowed func(ctx context.Context) (bool, string)
	Client  *http.Client
	Log     *slog.Logger
	Now     func() time.Time
	// StartDelay: the first check waits this long after Run starts, so it
	// never competes with startup.
	StartDelay time.Duration
	// RetryWait is the pause before a download's second attempt (doubled
	// before the third).
	RetryWait time.Duration
	// MaxDownload and MaxDatabase replace MaxDownloadSize and
	// MaxDatabaseSize (tests).
	MaxDownload, MaxDatabase int64
}

// Updater downloads and updates the databases. One check runs at a time.
type Updater struct {
	cfg  UpdaterConfig
	e    *Enricher
	kick chan struct{}

	checkMu sync.Mutex // held for a whole check

	mu       sync.Mutex
	st       updState
	checking bool
	next     time.Time
	allowed  bool
	reason   string
}

// dbState is one downloaded database.
type dbState struct {
	Release      string    `json:"release"` // "2026-10"
	BuildEpoch   uint64    `json:"build_epoch"`
	SHA256       string    `json:"sha256"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Downloaded   time.Time `json:"downloaded"`
}

// updState is kept next to the downloads (state.json), so a restart
// neither forgets nor downloads again what it has.
type updState struct {
	DBs         map[string]*dbState `json:"databases"`
	LastCheck   time.Time           `json:"last_check,omitzero"`
	LastSuccess time.Time           `json:"last_success,omitzero"`
	LastError   string              `json:"last_error,omitempty"`
	LastErrorAt time.Time           `json:"last_error_at,omitzero"`
}

// UpdateStatus describes automatic downloads.
type UpdateStatus struct {
	// Enabled: DROOL downloads and updates databases now; else Disabled
	// says why not.
	Enabled     bool   `json:"enabled"`
	Disabled    string `json:"disabled,omitempty"`
	Provider    string `json:"provider"`
	ProviderURL string `json:"provider_url"`
	License     string `json:"license"`
	Attribution string `json:"attribution"`
	Directory   string `json:"directory"`
	// Checking: a check or download is running.
	Checking    bool   `json:"checking"`
	LastCheck   string `json:"last_check,omitempty"`
	LastSuccess string `json:"last_success,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
	NextCheck   string `json:"next_check,omitempty"`
	// Databases DROOL downloaded.
	Databases []DownloadedDB `json:"databases"`
	// Yours: kinds the operator's own databases provide (not downloaded).
	Yours []string `json:"yours"`
}

// DownloadedDB is one downloaded database.
type DownloadedDB struct {
	Kind       string `json:"kind"`
	Release    string `json:"release"`
	BuildDate  string `json:"build_date,omitempty"`
	Downloaded string `json:"downloaded"`
}

// NewUpdater prepares automatic downloads into cfg.Dir for e (which must
// have that directory as its download directory). Run starts it.
func NewUpdater(cfg UpdaterConfig, e *Enricher) *Updater {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultDownloadURL
	}
	if !strings.HasSuffix(cfg.BaseURL, "/") {
		cfg.BaseURL += "/"
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Allowed == nil {
		cfg.Allowed = func(context.Context) (bool, string) { return true, "" }
	}
	if cfg.Client == nil {
		cfg.Client = defaultClient()
	}
	if cfg.RetryWait == 0 {
		cfg.RetryWait = 10 * time.Second
	}
	if cfg.MaxDownload == 0 {
		cfg.MaxDownload = MaxDownloadSize
	}
	if cfg.MaxDatabase == 0 {
		cfg.MaxDatabase = MaxDatabaseSize
	}
	u := &Updater{cfg: cfg, e: e, kick: make(chan struct{}, 1), st: updState{DBs: map[string]*dbState{}}, allowed: true}
	// Downloads a crash interrupted: only DROOL's own temporary files.
	if left, _ := filepath.Glob(filepath.Join(cfg.Dir, ".*.tmp")); len(left) > 0 {
		for _, f := range left {
			os.Remove(f)
		}
	}
	if b, err := os.ReadFile(filepath.Join(cfg.Dir, "state.json")); err == nil {
		var st updState
		if json.Unmarshal(b, &st) == nil {
			if st.DBs == nil {
				st.DBs = map[string]*dbState{}
			}
			u.st = st
		}
	}
	return u
}

func defaultClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   5 * time.Minute, // the whole download
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != via[0].URL.Scheme {
				return errors.New("redirected away from HTTPS")
			}
			return nil
		},
	}
}

// fileName is where a kind's downloaded database lives.
func fileName(kind string) string { return "dbip-" + kind + "-lite.mmdb" }

// Run checks shortly after it starts — unless what it needs was checked
// within the last day — then daily (hourly while nothing could be
// downloaded yet), and whenever Trigger asks, until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	wait := u.firstWait()
	for {
		u.mu.Lock()
		u.next = u.cfg.Now().Add(wait)
		u.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-u.kick:
			t.Stop()
		}
		if err := u.Check(ctx); err != nil && !errors.Is(err, ErrChecking) && ctx.Err() == nil {
			u.cfg.Log.Warn("GeoIP/ASN database update failed; the databases in use are kept", "provider", ProviderName, "err", err)
		}
		wait = u.nextWait()
	}
}

// Trigger asks Run for a check now (Check now, or downloads were just
// turned on). It never blocks.
func (u *Updater) Trigger() {
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

// firstWait: soon after startup when a database is missing or was never
// checked; otherwise a day after the last successful check.
func (u *Updater) firstWait() time.Duration {
	have := u.e.OperatorKinds()
	u.mu.Lock()
	last := u.st.LastSuccess
	missing := false
	for _, k := range []string{KindCountry, KindASN} {
		if !have[k] && u.st.DBs[k] == nil {
			missing = true
		}
	}
	u.mu.Unlock()
	if last.IsZero() || missing {
		return u.cfg.StartDelay
	}
	for _, k := range []string{KindCountry, KindASN} {
		if !have[k] && !u.e.downloadedLoads(fileName(k), k) {
			return u.cfg.StartDelay // damaged or removed on disk
		}
	}
	d := checkEvery - u.cfg.Now().Sub(last)
	return min(max(d, u.cfg.StartDelay), checkEvery)
}

func (u *Updater) nextWait() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.allowed && u.st.LastError != "" && !u.st.LastErrorAt.Before(u.st.LastSuccess) {
		have := u.e.OperatorKinds()
		for _, k := range []string{KindCountry, KindASN} {
			if u.st.DBs[k] == nil && !have[k] {
				return retryMissing
			}
		}
	}
	return checkEvery
}

// Check looks for newer databases now and installs them. It returns
// ErrChecking if a check is already running, and what failed otherwise
// (the databases in use stay).
func (u *Updater) Check(ctx context.Context) error {
	if !u.checkMu.TryLock() {
		return ErrChecking
	}
	defer u.checkMu.Unlock()
	ok, why := u.cfg.Allowed(ctx)
	u.mu.Lock()
	u.allowed, u.reason = ok, why
	if ok {
		u.checking = true
	}
	u.mu.Unlock()
	if !ok {
		return nil
	}
	defer func() {
		u.mu.Lock()
		u.checking = false
		u.mu.Unlock()
	}()
	if err := os.MkdirAll(u.cfg.Dir, 0o755); err != nil {
		return u.finish(nil, false, fmt.Errorf("can't create %s: %w", u.cfg.Dir, err))
	}
	now := u.cfg.Now()
	yours := u.e.OperatorKinds()
	var errs []error
	changed := false
	for _, kind := range []string{KindCountry, KindASN} {
		if yours[kind] {
			continue
		}
		c, err := u.update(ctx, kind, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s database: %w", kindWord(kind), err))
		}
		changed = changed || c
	}
	return u.finish(&now, changed, errors.Join(errs...))
}

// finish records a check's outcome and loads what changed.
func (u *Updater) finish(at *time.Time, changed bool, err error) error {
	if changed {
		u.e.Reload()
	}
	now := u.cfg.Now()
	if at != nil {
		now = *at
	}
	u.mu.Lock()
	u.st.LastCheck = now
	if err == nil {
		u.st.LastSuccess, u.st.LastError, u.st.LastErrorAt = now, "", time.Time{}
	} else {
		u.st.LastError, u.st.LastErrorAt = oneLine(err), now
	}
	st := u.st
	u.mu.Unlock()
	if serr := u.save(st); serr != nil && err == nil {
		err = serr
	}
	return err
}

func kindWord(kind string) string {
	if kind == KindASN {
		return "ASN"
	}
	return "country"
}

func release(t time.Time) string { return t.UTC().Format("2006-01") }

// update brings one kind's database up to date: this month's release, or
// last month's early on the first before DB-IP publishes the new one.
func (u *Updater) update(ctx context.Context, kind string, now time.Time) (bool, error) {
	u.mu.Lock()
	cur := u.st.DBs[kind]
	u.mu.Unlock()
	if cur != nil && !u.e.downloadedLoads(fileName(kind), kind) {
		cur = nil // gone or damaged on disk: download it again
	}
	month := release(now)
	got, err := u.download(ctx, kind, month, cur)
	if errors.Is(err, errNotPublished) {
		y, m, _ := now.UTC().Date()
		prev := release(time.Date(y, m-1, 1, 0, 0, 0, 0, time.UTC))
		if cur != nil && cur.Release >= prev {
			return false, nil // nothing newer published yet
		}
		month = prev
		got, err = u.download(ctx, kind, month, cur)
		if errors.Is(err, errNotPublished) {
			return false, fmt.Errorf("no release for %s or %s at %s", prev, release(now), u.cfg.BaseURL)
		}
	}
	if err != nil || got == nil {
		return false, err // failed, or not modified
	}
	defer os.Remove(got.tmp)
	if cur != nil && got.state.BuildEpoch < cur.BuildEpoch {
		return false, fmt.Errorf("the download (built %s) is older than the database in use", time.Unix(int64(got.state.BuildEpoch), 0).UTC().Format("2006-01-02"))
	}
	final := filepath.Join(u.cfg.Dir, fileName(kind))
	if err := os.Rename(got.tmp, final); err != nil {
		return false, err
	}
	syncDir(u.cfg.Dir)
	got.state.Release = month
	got.state.Downloaded = u.cfg.Now().UTC()
	u.mu.Lock()
	u.st.DBs[kind] = &got.state
	u.mu.Unlock()
	u.cfg.Log.Info("GeoIP/ASN database downloaded", "kind", kind, "release", month, "provider", ProviderName)
	return true, nil
}

type fetched struct {
	tmp   string
	state dbState
}

// download fetches and validates one release, retrying transient failures
// a bounded number of times. It returns nil, nil when the release is the
// one installed and unchanged.
func (u *Updater) download(ctx context.Context, kind, month string, cur *dbState) (*fetched, error) {
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			t := time.NewTimer(u.cfg.RetryWait << (i - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		f, err := u.fetch(ctx, kind, month, cur)
		if err == nil || errors.Is(err, errNotPublished) || errors.Is(err, errPermanent) || ctx.Err() != nil {
			return f, err
		}
		last = err
	}
	return nil, last
}

var errPermanent = errors.New("permanent")

type permanentError struct{ msg string }

func (e permanentError) Error() string        { return e.msg }
func (e permanentError) Is(target error) bool { return target == errPermanent }

func (u *Updater) fetch(ctx context.Context, kind, month string, cur *dbState) (*fetched, error) {
	src := u.cfg.BaseURL + "dbip-" + kind + "-lite-" + month + ".mmdb.gz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, permanentError{err.Error()}
	}
	req.Header.Set("User-Agent", "DROOL (GeoIP database updater)")
	if cur != nil && cur.Release == month {
		// Unchanged since the last download: no new copy (304).
		if cur.ETag != "" {
			req.Header.Set("If-None-Match", cur.ETag)
		}
		if cur.LastModified != "" {
			req.Header.Set("If-Modified-Since", cur.LastModified)
		}
	}
	resp, err := u.cfg.Client.Do(req)
	if err != nil {
		return nil, netError(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		return nil, nil
	case http.StatusNotFound:
		return nil, errNotPublished
	default:
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	maxDL, maxDB := u.cfg.MaxDownload, u.cfg.MaxDatabase
	if resp.ContentLength > maxDL {
		return nil, permanentError{fmt.Sprintf("the download is too large (%d bytes; the limit is %d)", resp.ContentLength, maxDL)}
	}
	body := &countingReader{r: io.LimitReader(resp.Body, maxDL+1)}
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, fmt.Errorf("not a gzip file: %w", err)
	}
	tmp, err := os.CreateTemp(u.cfg.Dir, ".download-*.tmp")
	if err != nil {
		return nil, permanentError{err.Error()}
	}
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	// gzip checks its CRC-32 and length at the end of the stream.
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(gz, maxDB+1))
	switch {
	case body.n > maxDL:
		return nil, permanentError{fmt.Sprintf("the download is larger than %d bytes", maxDL)}
	case n > maxDB:
		return nil, permanentError{fmt.Sprintf("the database is larger than %d bytes", maxDB)}
	case err != nil:
		return nil, fmt.Errorf("download incomplete or damaged: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	meta, err := validate(tmp.Name(), kind)
	if err != nil {
		return nil, permanentError{"the downloaded database is not usable: " + err.Error()}
	}
	keep = true
	return &fetched{tmp: tmp.Name(), state: dbState{BuildEpoch: meta.BuildEpoch, SHA256: hex.EncodeToString(h.Sum(nil)),
		ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}}, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// probes are well-known public addresses every country and ASN database
// answers for.
var probes = []string{"8.8.8.8", "1.1.1.1", "9.9.9.9", "2001:4860:4860::8888"}

// validate checks a downloaded database: a MaxMind DB that DROOL reads,
// with the kind of data expected, answering for well-known addresses.
func validate(path, kind string) (Meta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	db, err := Open(b)
	if err != nil {
		return Meta{}, err
	}
	for _, s := range probes {
		v, err := db.Lookup(netip.MustParseAddr(s))
		if err != nil || v == nil {
			continue
		}
		i := decodeInfo(v)
		if (kind == KindCountry && i.CountryCode != "") || (kind == KindASN && i.ASN != 0) {
			return db.Meta(), nil
		}
	}
	return Meta{}, fmt.Errorf("it has no %s data (database type %q)", kindWord(kind), db.Meta().DatabaseType)
}

// netError keeps a failed request's reason readable: the host and why.
func netError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		host := ue.URL
		if p, perr := url.Parse(ue.URL); perr == nil {
			host = p.Host
		}
		return fmt.Errorf("can't reach %s: %w", host, ue.Err)
	}
	return err
}

func oneLine(err error) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(err.Error(), "\n", "; ")), " ")
}

func (u *Updater) save(st updState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(u.cfg.Dir, ".state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(u.cfg.Dir, "state.json")); err != nil {
		return err
	}
	syncDir(u.cfg.Dir)
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Status describes automatic downloads now.
func (u *Updater) Status(ctx context.Context) UpdateStatus {
	ok, why := u.cfg.Allowed(ctx)
	yours := u.e.OperatorKinds()
	u.mu.Lock()
	defer u.mu.Unlock()
	ts := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	st := UpdateStatus{Enabled: ok, Disabled: why, Provider: ProviderName, ProviderURL: ProviderURL, License: ProviderLicense,
		Attribution: Attribution, Directory: u.cfg.Dir, Checking: u.checking, LastCheck: ts(u.st.LastCheck),
		LastSuccess: ts(u.st.LastSuccess), LastError: u.st.LastError, LastErrorAt: ts(u.st.LastErrorAt), Databases: []DownloadedDB{}, Yours: []string{}}
	if ok {
		st.NextCheck = ts(u.next)
	}
	for _, k := range []string{KindCountry, KindASN} {
		if yours[k] {
			st.Yours = append(st.Yours, k)
		}
		if d := u.st.DBs[k]; d != nil {
			db := DownloadedDB{Kind: k, Release: d.Release, Downloaded: ts(d.Downloaded)}
			if d.BuildEpoch > 0 {
				db.BuildDate = time.Unix(int64(d.BuildEpoch), 0).UTC().Format("2006-01-02")
			}
			st.Databases = append(st.Databases, db)
		}
	}
	return st
}

// Pending reports whether the updater will provide kind but hasn't yet:
// downloads are on, the operator provides no such database, nothing is
// downloaded, and no attempt has failed since the last success (the first
// download is still being prepared).
func (u *Updater) Pending(ctx context.Context, kind string) bool {
	if ok, _ := u.cfg.Allowed(ctx); !ok || u.e.OperatorKinds()[kind] {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st.DBs[kind] == nil && (u.checking || u.st.LastError == "")
}
