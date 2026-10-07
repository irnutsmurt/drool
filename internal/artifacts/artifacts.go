// Package artifacts is the controller's content-addressed blob store
// (ARCHITECTURE-ALPHA §10.7): file contents for deployments and the previous
// versions kept for rollback. Blobs live under <data_dir>/artifacts/sha256/
// named by their SHA-256; SQLite holds the index. Jobs, deployment records
// and audit events refer to blobs by hash, never by copying content.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"drool/internal/files"
	"drool/internal/store"
	"drool/internal/store/db"
)

// DirName is the store's directory inside the data directory.
const DirName = "artifacts"

// MaxSize bounds one artifact (they are kept on the controller's disk).
const MaxSize = 1 << 30

// ErrNotFound means no artifact has that hash.
var ErrNotFound = errors.New("artifact not found")

// Artifact is a stored blob's metadata.
type Artifact struct {
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Name      string    `json:"name"`
	IsText    bool      `json:"is_text"`
	CreatedAt time.Time `json:"created_at"`
}

// Store reads and writes artifacts.
type Store struct {
	root string // <data_dir>/artifacts
	st   *store.Store
	now  func() time.Time
}

// Open prepares the store under dataDir.
func Open(dataDir string, st *store.Store) (*Store, error) {
	root := filepath.Join(dataDir, DirName)
	for _, d := range []string{root, filepath.Join(root, "sha256"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("artifact store: %w", err)
		}
	}
	return &Store{root: root, st: st, now: time.Now}, nil
}

// ValidHash reports whether s is a lowercase hex SHA-256.
func ValidHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// RelPath is an artifact's path relative to the data directory (also its
// name inside backups).
func RelPath(sha string) string { return filepath.Join(DirName, "sha256", sha[:2], sha) }

func (s *Store) path(sha string) string { return filepath.Join(s.root, "sha256", sha[:2], sha) }

// Put streams r into the store. The same content is stored once.
func (s *Store) Put(ctx context.Context, r io.Reader, name, userID string) (*Artifact, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "put-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	var head bytes.Buffer // enough to tell whether it is editable text
	n, err := io.Copy(io.MultiWriter(tmp, h, limitWriter{&head, files.MaxTextBytes + 1}), io.LimitReader(ctxReader{ctx, r}, MaxSize+1))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if n > MaxSize {
		return nil, files.Errorf(files.CodeTooLarge, "files larger than %d MiB can't be stored for deployment", MaxSize>>20)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	dst := s.path(sha)
	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp.Name(), dst); err != nil {
			return nil, err
		}
	}
	now := s.now()
	a := &Artifact{SHA256: sha, Size: n, Name: cleanName(name), IsText: files.IsText(head.Bytes()), CreatedAt: now}
	if err := s.st.W.UpsertArtifact(ctx, db.UpsertArtifactParams{Sha256: sha, Size: n, Name: a.Name, IsText: boolInt(a.IsText),
		CreatedBy: nullStr(userID), CreatedAt: now.UnixMilli(), LastUsedAt: now.UnixMilli()}); err != nil {
		return nil, err
	}
	return s.Get(ctx, sha)
}

func cleanName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	if n == "." || n == "/" {
		return ""
	}
	if len(n) > 255 {
		n = n[:255]
	}
	return n
}

// Get returns an artifact's metadata.
func (s *Store) Get(ctx context.Context, sha string) (*Artifact, error) {
	if !ValidHash(sha) {
		return nil, ErrNotFound
	}
	row, err := s.st.R.GetArtifact(ctx, sha)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Artifact{SHA256: row.Sha256, Size: row.Size, Name: row.Name, IsText: row.IsText != 0,
		CreatedAt: time.UnixMilli(row.CreatedAt).UTC()}, nil
}

// Open streams an artifact. The content is checked against its hash as it
// is read: a corrupted blob fails at the end instead of being deployed.
func (s *Store) Open(ctx context.Context, sha string) (io.ReadCloser, *Artifact, error) {
	a, err := s.Get(ctx, sha)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(s.path(sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("%w: %s is indexed but missing from the store", ErrNotFound, sha)
	}
	if err != nil {
		return nil, nil, err
	}
	_ = s.st.W.TouchArtifact(ctx, db.TouchArtifactParams{LastUsedAt: s.now().UnixMilli(), Sha256: sha})
	return &verifyingReader{f: f, h: sha256.New(), want: sha}, a, nil
}

// ReadText returns a text artifact's content (for diffs).
func (s *Store) ReadText(ctx context.Context, sha string) (string, bool, error) {
	rc, a, err := s.Open(ctx, sha)
	if err != nil {
		return "", false, err
	}
	defer rc.Close()
	if !a.IsText {
		return "", false, nil
	}
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// GC removes artifacts that nothing references and that weren't used for
// minAge, plus stray temporary files. It returns how many were removed.
func (s *Store) GC(ctx context.Context, minAge time.Duration) (int, error) {
	cutoff := s.now().Add(-minAge)
	rows, err := s.st.R.ListUnreferencedArtifacts(ctx, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		// Delete the index row first: a concurrent Put of the same content
		// re-creates both.
		if err := s.st.W.DeleteArtifact(ctx, r.Sha256); err != nil {
			continue // referenced meanwhile (foreign key)
		}
		if err := os.Remove(s.path(r.Sha256)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
		n++
	}
	tmps, _ := filepath.Glob(filepath.Join(s.root, "tmp", "put-*"))
	for _, t := range tmps {
		if fi, err := os.Stat(t); err == nil && fi.ModTime().Before(cutoff) {
			os.Remove(t)
		}
	}
	return n, nil
}

type verifyingReader struct {
	f *os.File
	h interface {
		Sum([]byte) []byte
		Write([]byte) (int, error)
	}
	want string
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.f.Read(p)
	v.h.Write(p[:n])
	if errors.Is(err, io.EOF) && hex.EncodeToString(v.h.Sum(nil)) != v.want {
		return n, files.Errorf(files.CodeIntegrity, "stored artifact %s is corrupted", v.want)
	}
	return n, err
}

func (v *verifyingReader) Close() error { return v.f.Close() }

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l limitWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
