package main

import (
	"reflect"
	"testing"

	"drool/internal/config"
)

// The startup message names the port browsers use: server.public_port
// when a container publishes the web port under another one.
func TestDisplayURLsUseThePublicWebPort(t *testing.T) {
	l, err := config.Load("", false, []string{"DROOL_SERVER_BIND=127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := displayURLs(l, "https"); !reflect.DeepEqual(got, []string{"https://127.0.0.1:8080"}) {
		t.Fatalf("default: %v", got)
	}
	l.Server.PublicPort = 8443
	if got := displayURLs(l, "https"); !reflect.DeepEqual(got, []string{"https://127.0.0.1:8443"}) {
		t.Fatalf("published as 8443: %v", got)
	}
}
