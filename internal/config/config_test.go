package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsWithoutFile(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Server.WebPort != 8080 || l.Server.TLS.Mode != "self_signed" {
		t.Fatalf("unexpected defaults: %+v", l.Server)
	}
	if l.Sources["server.web_port"] != SourceDefault {
		t.Fatalf("source = %s", l.Sources["server.web_port"])
	}
	if l.FilePath != "" {
		t.Fatalf("FilePath = %q", l.FilePath)
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), true, nil); err == nil {
		t.Fatal("expected error for explicit missing config file")
	}
}

func TestPrecedenceAndSources(t *testing.T) {
	p := writeFile(t, `
server:
  web_port: 9000
  bind: 127.0.0.1
poller:
  status_interval: 10m
`)
	env := []string{
		"DROOL_SERVER_WEB_PORT=9443",
		"DROOL_SERVER_TRUSTED_PROXIES=10.0.0.1, 192.168.0.0/16",
		"DROOL_LOGGING_COMPRESS=bogus-ignored-unknown-key",
		"UNRELATED=1",
	}
	l, err := Load(p, true, env)
	if err != nil {
		t.Fatal(err)
	}
	if l.Server.WebPort != 9443 || l.Sources["server.web_port"] != SourceEnv {
		t.Fatalf("env should override file: port=%d source=%s", l.Server.WebPort, l.Sources["server.web_port"])
	}
	if l.Server.Bind != "127.0.0.1" || l.Sources["server.bind"] != SourceFile {
		t.Fatalf("bind=%q source=%s", l.Server.Bind, l.Sources["server.bind"])
	}
	if l.Poller.StatusInterval != 10*time.Minute || l.Sources["poller.status_interval"] != SourceFile {
		t.Fatalf("status_interval=%s", l.Poller.StatusInterval)
	}
	if got := l.Server.TrustedProxies; len(got) != 2 || got[1] != "192.168.0.0/16" {
		t.Fatalf("trusted_proxies=%v", got)
	}
	if l.Sources["engine.global_concurrency"] != SourceDefault {
		t.Fatal("untouched key should be default")
	}
	var found bool
	for _, v := range l.Values() {
		if v.Key == "server.web_port" {
			found = true
			if v.EnvVar != "DROOL_SERVER_WEB_PORT" || v.Source != SourceEnv {
				t.Fatalf("value = %+v", v)
			}
		}
	}
	if !found {
		t.Fatal("server.web_port missing from Values()")
	}
}

func TestUnknownFileKeyRejected(t *testing.T) {
	p := writeFile(t, "server:\n  web_prot: 1\n")
	if _, err := Load(p, true, nil); err == nil || !strings.Contains(err.Error(), "web_prot") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestBadEnvValue(t *testing.T) {
	if _, err := Load("", false, []string{"DROOL_SERVER_WEB_PORT=abc"}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestPlaintextRequiresLoopback(t *testing.T) {
	cases := []struct {
		name    string
		env     []string
		wantErr bool
	}{
		{"public bind rejected", []string{"DROOL_SERVER_TLS_MODE=off", "DROOL_SERVER_BIND=0.0.0.0"}, true},
		{"trusted proxies do not help", []string{"DROOL_SERVER_TLS_MODE=off", "DROOL_SERVER_BIND=0.0.0.0",
			"DROOL_SERVER_TRUSTED_PROXIES=10.0.0.0/8"}, true},
		{"ipv4 loopback ok", []string{"DROOL_SERVER_TLS_MODE=off", "DROOL_SERVER_BIND=127.0.0.1"}, false},
		{"ipv6 loopback ok", []string{"DROOL_SERVER_TLS_MODE=off", "DROOL_SERVER_BIND=::1"}, false},
		{"explicit opt-in", []string{"DROOL_SERVER_TLS_MODE=off", "DROOL_SERVER_BIND=0.0.0.0",
			"DROOL_SERVER_ALLOW_INSECURE_PLAINTEXT=true"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := Load("", false, c.env)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && c.name == "explicit opt-in" && !l.IsInsecurePlaintext() {
				t.Fatal("IsInsecurePlaintext should be true")
			}
		})
	}
}

func TestProvidedTLSRequiresFiles(t *testing.T) {
	if _, err := Load("", false, []string{"DROOL_SERVER_TLS_MODE=provided"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetupSecretRedactedAndValidated(t *testing.T) {
	l, err := Load("", false, []string{"DROOL_SETUP_SECRET=super-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range l.Values() {
		if v.Key == "setup.secret" && v.Value != "«redacted»" {
			t.Fatalf("setup.secret displayed: %v", v.Value)
		}
	}
	if s, _ := l.ProvidedSetupSecret(); s != "super-secret-value" {
		t.Fatalf("ProvidedSetupSecret = %q", s)
	}
	p := writeFile(t, "  from-file-secret-123\n")
	l, err = Load("", false, []string{"DROOL_SETUP_SECRET_FILE=" + p})
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := l.ProvidedSetupSecret(); s != "from-file-secret-123" {
		t.Fatalf("secret file = %q", s)
	}
	if _, err := Load("", false, []string{"DROOL_SETUP_SECRET=a", "DROOL_SETUP_SECRET_FILE=" + p}); err == nil {
		t.Fatal("both secret and secret_file accepted")
	}
}
