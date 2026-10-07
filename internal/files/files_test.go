package files

import (
	"strings"
	"testing"
)

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"/etc/../etc/hosts": "/etc/hosts", "/": "/", "/a//b/": "/a/b"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "etc/hosts", "../x", "/a\x00b", "/a\nb"} {
		if _, err := CleanPath(bad); err == nil {
			t.Errorf("CleanPath(%q) accepted", bad)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00"} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("CleanName(%q) accepted", bad)
		}
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]uint32{"644": 0o644, "0755": 0o755, "4755": 0o4755, "0": 0} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %o, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "8", "rwx", "77777", "-1"} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("ParseMode(%q) accepted", bad)
		}
	}
}

func TestIsText(t *testing.T) {
	if !IsText([]byte("héllo\n")) || IsText([]byte("a\x00b")) || IsText([]byte{0xff, 0xfe}) {
		t.Fatal("IsText misclassified")
	}
}

func TestDiff(t *testing.T) {
	a := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	b := strings.Replace(a, "three\n", "THREE\n", 1) + "eleven\n"
	d, ok := Diff("a", "b", a, b)
	if !ok {
		t.Fatal("diff refused")
	}
	for _, want := range []string{"--- a\n+++ b\n", "-three\n+THREE\n", "+eleven\n", "@@ -1,6 +1,6 @@"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if d, _ := Diff("a", "b", a, a); d != "" {
		t.Errorf("equal texts gave a diff: %q", d)
	}
	d, _ = Diff("a", "b", "", "new\n")
	if !strings.Contains(d, "@@ -0,0 +1 @@\n+new\n") {
		t.Errorf("creation diff: %q", d)
	}
	d, _ = Diff("a", "b", "x", "y")
	if !strings.Contains(d, "\\ No newline at end of file") {
		t.Errorf("missing final newline not marked: %q", d)
	}
}
