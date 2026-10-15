package geoip

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Enricher serves lookups from the databases in one directory. Operators
// update the files with whatever tool they use; the enricher notices a
// changed, added or removed file (Refresh, called periodically) and swaps
// databases atomically. A missing directory, no databases or a corrupt file
// leave enrichment unavailable — never failing anything else.
type Enricher struct {
	dir   string
	cache *Cache

	refreshMu sync.Mutex // one refresh at a time; good is only touched under it

	mu      sync.RWMutex
	country Provider
	asn     Provider
	files   []FileStatus
	sig     string                   // name/size/mtime of every file, to detect changes
	good    map[string]*mmdbProvider // last good load of each file
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
}

// Status is the enricher's state.
type Status struct {
	Directory string       `json:"directory"`
	Country   bool         `json:"country"` // a country database is loaded
	ASN       bool         `json:"asn"`
	Files     []FileStatus `json:"files"`
	LoadedAt  string       `json:"loaded_at,omitempty"`
	Error     string       `json:"error,omitempty"`
	Cache     Stats        `json:"cache"`
}

// MaxFileSize bounds a database file (they are read into memory). City
// databases are ~70 MB; country and ASN ones are a few MB.
const MaxFileSize = 512 << 20

// NewEnricher prepares an enricher over dir; call Refresh to load.
func NewEnricher(dir string, cacheSize int) *Enricher {
	return &Enricher{dir: dir, cache: NewCache(cacheSize), good: map[string]*mmdbProvider{}}
}

// Dir returns the database directory.
func (e *Enricher) Dir() string { return e.dir }

func (e *Enricher) signature() (string, []fs.FileInfo) {
	ents, err := os.ReadDir(e.dir)
	if err != nil {
		return "missing", nil
	}
	var infos []fs.FileInfo
	var b strings.Builder
	for _, en := range ents {
		if en.IsDir() || !strings.HasSuffix(strings.ToLower(en.Name()), ".mmdb") {
			continue
		}
		fi, err := en.Info()
		if err != nil {
			continue
		}
		infos = append(infos, fi)
		fmt.Fprintf(&b, "%s|%d|%d\n", fi.Name(), fi.Size(), fi.ModTime().UnixNano())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	return b.String(), infos
}

// Refresh reloads the databases when the directory's contents changed. It
// reports whether anything was reloaded.
func (e *Enricher) Refresh() bool { return e.refresh(false) }

// Reload loads the databases again even if nothing seems to have changed.
func (e *Enricher) Reload() { e.refresh(true) }

func (e *Enricher) refresh(force bool) bool {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	sig, infos := e.signature()
	e.mu.RLock()
	same := sig == e.sig
	e.mu.RUnlock()
	if same && !force {
		return false
	}
	type cand struct {
		p     *mmdbProvider
		fs    int
		epoch uint64
	}
	var files []FileStatus
	var countries, asns []cand
	good := map[string]*mmdbProvider{}
	errMsg := ""
	if sig == "missing" {
		errMsg = "the directory does not exist"
	}
	for _, fi := range infos {
		st := FileStatus{Name: fi.Name(), Size: fi.Size(), Modified: fi.ModTime().UTC().Format(time.RFC3339), Provides: []string{}, Used: []string{}}
		var p *mmdbProvider
		db, err := e.open(filepath.Join(e.dir, fi.Name()), fi.Size())
		if err != nil {
			st.Error = err.Error()
			// A file replaced by a broken or half-written copy: keep
			// serving the version that loaded before.
			if p = e.good[fi.Name()]; p == nil {
				files = append(files, st)
				continue
			}
			st.Error += " (still using the previously loaded version)"
			db = p.db
		} else {
			p = &mmdbProvider{db: db, kinds: detectKinds(db)}
		}
		good[fi.Name()] = p
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
			c := cand{p: p, fs: len(files) - 1, epoch: db.Meta().BuildEpoch}
			if k == KindCountry {
				countries = append(countries, c)
			} else {
				asns = append(asns, c)
			}
		}
	}
	// The newest database of each kind wins; prefer a dedicated one when
	// build dates tie.
	pick := func(cs []cand, kind string) Provider {
		if len(cs) == 0 {
			return nil
		}
		sort.SliceStable(cs, func(i, j int) bool {
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
	e.country, e.asn, e.files, e.sig, e.errMsg, e.good = country, asn, files, sig, errMsg, good
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
	st := Status{Directory: e.dir, Country: e.country != nil, ASN: e.asn != nil, Files: append([]FileStatus{}, e.files...),
		Error: e.errMsg, Cache: e.cache.Stats()}
	if !e.loaded.IsZero() {
		st.LoadedAt = e.loaded.UTC().Format(time.RFC3339)
	}
	return st
}
