package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

// --master-key - takes the key file from standard input, ahead of the backup
// when that comes from standard input too: cat master.key backup | drool
// restore --master-key - -.
func TestSplitKeyAndBackup(t *testing.T) {
	const key = "# rotated 2026-10-12\n1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=\n2:ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8=\n"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("the backup"))
	zw.Close()
	backupBytes := gz.Bytes()

	check := func(name, in string, wantKey string) {
		t.Helper()
		k, rest, err := splitKeyAndBackup(strings.NewReader(in), true, "drool restore")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(k) != wantKey {
			t.Fatalf("%s: key %q", name, k)
		}
		got, _ := io.ReadAll(rest)
		if !bytes.Equal(got, backupBytes) {
			t.Fatalf("%s: the backup lost or gained bytes (%d, want %d)", name, len(got), len(backupBytes))
		}
		if zr, err := gzip.NewReader(bytes.NewReader(got)); err != nil {
			t.Fatalf("%s: %v", name, err)
		} else if b, _ := io.ReadAll(zr); string(b) != "the backup" {
			t.Fatalf("%s: backup content %q", name, b)
		}
	}
	check("key then backup", key+string(backupBytes), key)
	trimmed := strings.TrimSuffix(key, "\n")
	check("a key file without its last newline", trimmed+string(backupBytes), trimmed)

	refused := func(name string, in []byte, backupFollows bool, want string) {
		t.Helper()
		_, _, err := splitKeyAndBackup(bytes.NewReader(in), backupFollows, "drool restore")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want an error containing %q", name, err, want)
		}
	}
	refused("the backup without the key", backupBytes, true, "no master key file in front of it")
	refused("the key without the backup", []byte(key), true, "ended without a backup")
	refused("nothing", nil, false, "standard input is empty")
	refused("something that isn't a key file", bytes.Repeat([]byte("x"), maxKeyFile+10), true, "doesn't start with a master key file")

	// With the backup in a file, standard input is the key file alone.
	k, _, err := splitKeyAndBackup(strings.NewReader(key), false, "drool restore")
	if err != nil || string(k) != key {
		t.Fatalf("key alone: %q, %v", k, err)
	}
}
