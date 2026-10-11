package agentsvc

import (
	"reflect"
	"testing"
)

func TestBinaryDirs(t *testing.T) {
	for exe, want := range map[string][]string{
		"/usr/bin":       {"/usr/bin", "/usr/lib/drool"},             // native packages
		"/usr/local/bin": {"/usr/local/bin", "/usr/local/lib/drool"}, // a release archive installed by hand
	} {
		if got := binaryDirs(exe); !reflect.DeepEqual(got, want) {
			t.Errorf("binaryDirs(%q) = %v, want %v", exe, got, want)
		}
	}
}
