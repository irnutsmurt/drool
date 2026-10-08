package source

import (
	"context"
	"sort"
)

// Fake is an in-memory Source for tests: files, links, installed tools
// and canned helper results.
type Fake struct {
	Files  map[string]string
	Links  map[string]string
	Tools  map[string]bool
	Cmds   map[string]*Result // by Key(tool, args...)
	FS     map[string]*Statfs
	FSErr  map[string]error
	Called []string
}

// ReadFile implements Source.
func (f *Fake) ReadFile(p string) ([]byte, error) {
	if s, ok := f.Files[p]; ok {
		return []byte(s), nil
	}
	return nil, ErrNotFound
}

// Glob implements Source.
func (f *Fake) Glob(pattern string) []string {
	var all []string
	seen := map[string]bool{}
	add := func(p string) {
		// Directories implied by files count as existing paths.
		for q := p; q != "/" && q != "."; q = dir(q) {
			if !seen[q] {
				seen[q] = true
				all = append(all, q)
			}
		}
	}
	for p := range f.Files {
		add(p)
	}
	for p := range f.Links {
		add(p)
	}
	sort.Strings(all)
	return matchAll(pattern, all)
}

func dir(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "/"
}

// Link implements Source.
func (f *Fake) Link(p string) string { return f.Links[p] }

// Statfs implements Source.
func (f *Fake) Statfs(_ context.Context, m string) (*Statfs, error) {
	if err := f.FSErr[m]; err != nil {
		return nil, err
	}
	if s, ok := f.FS[m]; ok {
		return s, nil
	}
	return nil, ErrNotFound
}

// Has implements Source.
func (f *Fake) Has(tool string) bool { return f.Tools[tool] }

// Run implements Source.
func (f *Fake) Run(_ context.Context, tool string, args ...string) (*Result, error) {
	if err := CheckArgs(tool, args); err != nil {
		return nil, err
	}
	k := Key(tool, args...)
	f.Called = append(f.Called, k)
	if !f.Tools[tool] {
		return nil, ErrNotFound
	}
	if r, ok := f.Cmds[k]; ok {
		return r, nil
	}
	return &Result{Exit: 1}, nil
}
