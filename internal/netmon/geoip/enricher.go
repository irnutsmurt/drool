package geoip

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Enricher serves lookups from the databases in the operator's directory
// and, when DROOL downloads databases itself (Updater), in DROOL's own
// directory. Operators update their files with whatever tool they use; the
// enricher notices a changed, added or removed file (Refresh, called
// periodically) and swaps databases atomically. For each kind of data the
// operator's own databases win over downloaded ones. A missing directory,
// no databases or a corrupt file leave enrichment unavailable — never
// failing anything else.
type Enricher struct {
	dir       string
	downloads string // DROOL's own downloads; "" when it downloads nothing
	cache     *Cache

	refreshMu sync.Mutex // one refresh at a time; good is only touched under it

	mu      sync.RWMutex
	country Provider
	asn     Provider
	files   []FileStatus
	sig     string                   // name/size/mtime of every file, to detect changes
	good    map[string]*mmdbProvider // last good load of each file, by path
	yours   map[string]bool          // kinds the operator's own databases provide
	gen     uint64
	loaded  time.Time
	errMsg  string
}

// FileStatus describes one database file.
type FileStatus struct {
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	Modified     string   `json:"modified"`
	DatabaseType string   `json:"database_type,omitempty"`
	BuildDate    string   `json:"build_date,omitempty"`
	Provides     []string `json:"provides"` // country, asn
	Used         []string `json:"used"`     // the kinds DROOL takes from this file
	Error        string   `json:"error,omitempty"`
	// Downloaded: DROOL downloaded this file (its own directory), as
	// opposed to a database the operator installed.
	Downloaded bool `json:"downloaded,omitempty"`
}

// Status is the enricher's state.
type Status struct {
	Directory string `json:"directory"`
	// DownloadDirectory holds the databases DROOL downloads itself.
	DownloadDirectory string       `json:"download_directory,omitempty"`
	Country           bool         `json:"country"` // a country database is loaded
	ASN               bool         `json:"asn"`
	Files             []FileStatus `json:"files"`
	LoadedAt          string       `json:"loaded_at,omitempty"`
	Error             string       `json:"error,omitempty"`
	Cache             Stats        `json:"cache"`
}

// MaxFileSize bounds a database file (they are read into memory). City
// databases are ~70 MB; country and ASN ones are a few MB.
const MaxFileSize = 512 << 20

// NewEnricher prepares an enricher over dir; call Refresh to load.
func NewEnricher(dir string, cacheSize int) *Enricher {
	return &Enricher{dir: dir, cache: NewCache(cacheSize), good: map[string]*mmdbProvider{}, yours: map[string]bool{}}
}

// SetDownloadDir adds the directory DROOL's Updater downloads into. Call it
// before the first Refresh.
func (e *Enricher) SetDownloadDir(dir string) { e.downloads = dir }

// Dir returns the operator's database directory.
func (e *Enricher) Dir() string { return e.dir }

// dbFile is a database file found in one of the directories.
type dbFile struct {
	path       string
	info       fs.FileInfo
	downloaded bool
}

func (e *Enricher) signature() (string, []dbFile, bool) {
	var files []dbFile
	var b strings.Builder
	missing := false
	scan := func(dir string, downloaded bool) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			if !downloaded {
				missing = true
				b.WriteString("missing\n")
			}
			return
		}
		var found []dbFile
		for _, en := range ents {
			if en.IsDir() || !strings.HasSuffix(strings.ToLower(en.Name()), ".mmdb") {
				continue
			}
			fi, err := en.Info()
			if err != nil {
				continue
			}
			found = append(found, dbFile{path: filepath.Join(dir, fi.Name()), info: fi, downloaded: downloaded})
			fmt.Fprintf(&b, "%t|%s|%d|%d\n", downloaded, fi.Name(), fi.Size(), fi.ModTime().UnixNano())
		}
		sort.Slice(found, func(i, j int) bool { return found[i].info.Name() < found[j].info.Name() })
		files = append(files, found...)
	}
	scan(e.dir, false)
	if e.downloads != "" && e.downloads != e.dir {
		scan(e.downloads, true)
	}
	return b.String(), files, missing
}

// Refresh reloads the databases when the directory's contents changed. It
// reports whether anything was reloaded.
func (e *Enricher) Refresh() bool { return e.refresh(false) }

// Reload loads the databases again even if nothing seems to have changed.
func (e *Enricher) Reload() { e.refresh(true) }

func (e *Enricher) refresh(force bool) bool {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	sig, found, missing := e.signature()
	e.mu.RLock()
	same := sig == e.sig
	e.mu.RUnlock()
	if same && !force {
		return false
	}
	type cand struct {
		p          *mmdbProvider
		fs         int
		epoch      uint64
		downloaded bool
	}
	var files []FileStatus
	var countries, asns []cand
	good := map[string]*mmdbProvider{}
	yours := map[string]bool{}
	errMsg := ""
	if missing && e.downloads == "" {
		errMsg = "the directory does not exist"
	}
	for _, f := range found {
		fi := f.info
		st := FileStatus{Name: fi.Name(), Size: fi.Size(), Modified: fi.ModTime().UTC().Format(time.RFC3339), Provides: []string{}, Used: []string{},
			Downloaded: f.downloaded}
		var p *mmdbProvider
		db, err := e.open(f.path, fi.Size())
		if err != nil {
			st.Error = err.Error()
			// A file replaced by a broken or half-written copy: keep
			// serving the version that loaded before.
			if p = e.good[f.path]; p == nil {
				files = append(files, st)
				continue
			}
			st.Error += " (still using the previously loaded version)"
			db = p.db
		} else {
			p = &mmdbProvider{db: db, kinds: detectKinds(db)}
		}
		good[f.path] = p
		st.DatabaseType = db.Meta().DatabaseType
		if ep := db.Meta().BuildEpoch; ep > 0 {
			st.BuildDate = time.Unix(int64(ep), 0).UTC().Format("2006-01-02")
		}
		st.Provides = append(st.Provides, p.kinds...)
		if len(p.kinds) == 0 {
			st.Error = "no country or ASN data recognised in this database"
		}
		files = append(files, st)
		for _, k := range p.kinds {
			if !f.downloaded {
				yours[k] = true
			}
			c := cand{p: p, fs: len(files) - 1, epoch: db.Meta().BuildEpoch, downloaded: f.downloaded}
			if k == KindCountry {
				countries = append(countries, c)
			} else {
				asns = append(asns, c)
			}
		}
	}
	// The operator's own databases win over downloaded ones; then the
	// newest database of each kind wins; prefer a dedicated one when build
	// dates tie.
	pick := func(cs []cand, kind string) Provider {
		if len(cs) == 0 {
			return nil
		}
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].downloaded != cs[j].downloaded {
				return !cs[i].downloaded
			}
			if cs[i].epoch != cs[j].epoch {
				return cs[i].epoch > cs[j].epoch
			}
			return len(cs[i].p.kinds) < len(cs[j].p.kinds)
		})
		files[cs[0].fs].Used = append(files[cs[0].fs].Used, kind)
		return cs[0].p
	}
	country, asn := pick(countries, KindCountry), pick(asns, KindASN)
	e.mu.Lock()
	e.country, e.asn, e.files, e.sig, e.errMsg, e.good, e.yours = country, asn, files, sig, errMsg, good, yours
	e.gen++
	e.loaded = time.Now()
	e.mu.Unlock()
	return true
}

func (e *Enricher) open(path string, size int64) (*DB, error) {
	if size > MaxFileSize {
		return nil, errors.New("file too large")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Open(b)
}

// Kinds reports which kinds of database are loaded.
func (e *Enricher) Kinds() (country, asn bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.country != nil, e.asn != nil
}

// OperatorKinds reports which kinds of data the operator's own databases
// provide (DROOL then downloads nothing of that kind).
func (e *Enricher) OperatorKinds() map[string]bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := map[string]bool{}
	for k, v := range e.yours {
		out[k] = v
	}
	return out
}

// DownloadedInUse reports whether lookups use a database DROOL downloaded
// (its attribution is then shown with the data).
func (e *Enricher) DownloadedInUse() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, f := range e.files {
		if f.Downloaded && len(f.Used) > 0 {
			return true
		}
	}
	return false
}

// downloadedLoads reports whether DROOL's downloaded file name loads and
// provides kind (a file damaged on disk is downloaded again).
func (e *Enricher) downloadedLoads(name, kind string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, f := range e.files {
		if f.Downloaded && f.Name == name && f.Error == "" && slices.Contains(f.Provides, kind) {
			return true
		}
	}
	return false
}

// Available reports whether any database is loaded.
func (e *Enricher) Available() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.country != nil || e.asn != nil
}

// Lookup enriches a. Callers must only pass public addresses (netmon.IsPublic):
// private, loopback, link-local, multicast and documentation addresses are
// never looked up. Lookups are cached; errors (a corrupt record) read as
// unknown.
func (e *Enricher) Lookup(a netip.Addr) Info {
	a = a.Unmap()
	e.mu.RLock()
	country, asn, gen := e.country, e.asn, e.gen
	e.mu.RUnlock()
	if country == nil && asn == nil {
		return Info{}
	}
	if i, ok := e.cache.get(a, gen); ok {
		return i
	}
	var out Info
	if country != nil {
		if i, err := country.Lookup(a); err == nil {
			out.CountryCode, out.Country = i.CountryCode, i.Country
		}
	}
	if asn != nil {
		if i, err := asn.Lookup(a); err == nil {
			out.ASN, out.Org = i.ASN, i.Org
		}
	}
	e.cache.put(a, out, gen)
	return out
}

// Status describes the loaded databases.
func (e *Enricher) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	st := Status{Directory: e.dir, DownloadDirectory: e.downloads, Country: e.country != nil, ASN: e.asn != nil, Files: append([]FileStatus{}, e.files...),
		Error: e.errMsg, Cache: e.cache.Stats()}
	if !e.loaded.IsZero() {
		st.LoadedAt = e.loaded.UTC().Format(time.RFC3339)
	}
	return st
}
