// Package secrets stores credentials encrypted at rest (ARCHITECTURE-ALPHA §7).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// KeyFileName is the default master key file inside the data directory.
const KeyFileName = "master.key"

// Box encrypts and decrypts secret values with AES-256-GCM. The additional
// authenticated data binds each ciphertext to its secret id and kind, so
// ciphertexts cannot be swapped between rows.
type Box struct {
	keys    map[int][]byte
	current int
}

// LoadOrCreateBox reads the master key file, creating it with a fresh random
// key (mode 0600) if it does not exist. The file holds one "version:base64key"
// line per key version; the highest version encrypts new values.
//
// insecureMode reports whether the file is readable by group or others.
func LoadOrCreateBox(path string) (box *Box, created, insecureMode bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, false, false, err
		}
		line := "1:" + base64.StdEncoding.EncodeToString(key) + "\n"
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, false, false, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, false, false, fmt.Errorf("create master key: %w", err)
		}
		if _, err := f.WriteString(line); err != nil {
			f.Close()
			return nil, false, false, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, false, false, err
		}
		if err := f.Close(); err != nil {
			return nil, false, false, err
		}
		data, created = []byte(line), true
	} else if err != nil {
		return nil, false, false, fmt.Errorf("read master key: %w", err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		insecureMode = true
	}
	box, err = parseKeys(string(data))
	return box, created, insecureMode, err
}

// ParseKeyFile parses master key file contents.
func ParseKeyFile(data []byte) (*Box, error) { return parseKeys(string(data)) }

// KeyID is a short, non-secret fingerprint of the key set, used to tell
// whether a backup and a master key file belong together.
func (b *Box) KeyID() string {
	h := sha256.New()
	h.Write([]byte("drool-master-key-id"))
	for v := 1; v <= b.current; v++ {
		if k, ok := b.keys[v]; ok {
			fmt.Fprintf(h, "\x00%d:", v)
			h.Write(k)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func parseKeys(data string) (*Box, error) {
	b := &Box{keys: map[int][]byte{}}
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		v, enc, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("master key line %d: expected version:base64key", i+1)
		}
		ver, err := strconv.Atoi(v)
		if err != nil || ver < 1 {
			return nil, fmt.Errorf("master key line %d: invalid version", i+1)
		}
		key, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("master key line %d: key must be 32 bytes base64", i+1)
		}
		b.keys[ver] = key
		if ver > b.current {
			b.current = ver
		}
	}
	if b.current == 0 {
		return nil, errors.New("master key file contains no keys")
	}
	return b, nil
}

// NewBoxForTest returns a box with a random key.
func NewBoxForTest() *Box {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Box{keys: map[int][]byte{1: key}, current: 1}
}

// CurrentVersion is the key version new values are sealed with.
func (b *Box) CurrentVersion() int { return b.current }

// DeriveKey derives a 32-byte key for purpose from master key version v
// (0: the current version) with HKDF-SHA256. Derived keys are never stored:
// whoever holds the master key file can derive them again, nobody else can.
func (b *Box) DeriveKey(purpose string, v int) ([]byte, int, error) {
	if v == 0 {
		v = b.current
	}
	k, ok := b.keys[v]
	if !ok {
		return nil, 0, fmt.Errorf("master key version %d not available", v)
	}
	out, err := hkdf.Key(sha256.New, k, nil, purpose, 32)
	return out, v, err
}

func aad(id, kind string) []byte { return []byte("drool-secret\x00" + id + "\x00" + kind) }

func (b *Box) gcm(version int) (cipher.AEAD, error) {
	key, ok := b.keys[version]
	if !ok {
		return nil, fmt.Errorf("master key version %d not available", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext for the secret with the given id and kind.
func (b *Box) Seal(id, kind string, plaintext []byte) (ciphertext, nonce []byte, keyVersion int, err error) {
	g, err := b.gcm(b.current)
	if err != nil {
		return nil, nil, 0, err
	}
	nonce = make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, 0, err
	}
	return g.Seal(nil, nonce, plaintext, aad(id, kind)), nonce, b.current, nil
}

// Open decrypts a value sealed by Seal.
func (b *Box) Open(id, kind string, ciphertext, nonce []byte, keyVersion int) ([]byte, error) {
	g, err := b.gcm(keyVersion)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, nonce, ciphertext, aad(id, kind))
	if err != nil {
		return nil, errors.New("secret decryption failed (wrong master key or tampered data)")
	}
	return pt, nil
}
