package agentsvc

import (
	"strings"
	"testing"

	"drool/internal/config"
)

// Endpoints is the one resolver for where Shepherds connect. agent.port is
// only the listener: an address without a port gets agent.public_port
// when it is set (a container published under another host port).
func TestEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name      string
		public    int
		advertise []string
		webHost   string
		want      string
	}{
		{"default: the web address with the listen port", 0, nil, "192.168.1.83:8080", "192.168.1.83:9443"},
		{"published as 2043:9443", 2043, nil, "192.168.1.83:8080", "192.168.1.83:2043"},
		{"a web address without a port", 2043, nil, "nas.local", "nas.local:2043"},
		{"an IPv6 web address", 2043, nil, "[fd00::5]:8443", "[fd00::5]:2043"},
		{"no web address at all", 0, nil, "", "localhost:9443"},
		{"advertise without a port gets the public port", 2043, []string{"nas.example.net"}, "192.168.1.83:8080", "nas.example.net:2043"},
		{"advertise without a port, no public port", 0, []string{"nas.example.net"}, "192.168.1.83:8080", "nas.example.net:9443"},
		{"advertise with a port is used as it is", 2043, []string{"drool.example.net:7000"}, "192.168.1.83:8080", "drool.example.net:7000"},
		{"several, IPv6 included", 2043, []string{"10.10.0.10", "[fd00::10]", "fd00::11"}, "x", "10.10.0.10:2043,[fd00::10]:2043,[fd00::11]:2043"},
	} {
		s := &Service{cfg: config.Agent{Port: 9443, PublicPort: tc.public, Advertise: tc.advertise}}
		if got := strings.Join(s.Endpoints(tc.webHost), ","); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
		if s.ListenPort() != 9443 {
			t.Errorf("%s: the listener moved to %d", tc.name, s.ListenPort())
		}
		// What enrollment answers with: the configured entries only.
		if got := strings.Join(s.advertised(), ","); (len(tc.advertise) > 0) != (got != "") || len(tc.advertise) > 0 && got != tc.want {
			t.Errorf("%s: enrollment answers %q", tc.name, got)
		}
	}
}

// A new controller address (Shepherds → Controller address) follows the
// agent.advertise rule: without a port of its own, the public port, never
// the listener's.
func TestEndpointForANewControllerAddress(t *testing.T) {
	s := &Service{cfg: config.Agent{Port: 9443, PublicPort: 2043}}
	for in, want := range map[string]string{
		"nas.example.net":        "nas.example.net:2043",
		" 192.168.1.83 ":         "192.168.1.83:2043",
		"drool.example.net:7000": "drool.example.net:7000",
		"fd00::10":               "[fd00::10]:2043",
		"[fd00::10]":             "[fd00::10]:2043",
		"[fd00::10]:7000":        "[fd00::10]:7000",
	} {
		if got, err := s.Endpoint(in); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a b", "https://nas.example.net", "nas.example.net:", "nas.example.net:https", ":2043", "nas:0", "nas:70000", "[nas]:x"} {
		if got, err := s.Endpoint(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		} else if !strings.Contains(err.Error(), "another port than 2043") {
			t.Errorf("%q: %v", in, err)
		}
	}
	s = &Service{cfg: config.Agent{Port: 9443}}
	if got, _ := s.Endpoint("nas.example.net"); got != "nas.example.net:9443" {
		t.Errorf("without a public port: %q", got)
	}
}
