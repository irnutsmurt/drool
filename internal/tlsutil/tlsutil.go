// Package tlsutil prepares the HTTPS listener configuration, generating a
// self-signed certificate on first start when tls.mode=self_signed.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fleetmgr/internal/config"
)

// Result describes the TLS setup for startup logging.
type Result struct {
	Config      *tls.Config // nil when TLS is off
	Mode        string
	CertFile    string
	Fingerprint string // SHA-256 of the leaf certificate, colon-separated hex
	Generated   bool
}

// Setup returns the TLS configuration for the given server settings.
func Setup(srv config.Server, dataDir string) (*Result, error) {
	res := &Result{Mode: srv.TLS.Mode}
	var certFile, keyFile string
	switch srv.TLS.Mode {
	case "off":
		return res, nil
	case "provided":
		certFile, keyFile = srv.TLS.CertFile, srv.TLS.KeyFile
	case "self_signed":
		dir := filepath.Join(dataDir, "tls")
		certFile, keyFile = filepath.Join(dir, "self-signed.crt"), filepath.Join(dir, "self-signed.key")
		if _, err := os.Stat(certFile); errors.Is(err, os.ErrNotExist) {
			if err := generate(dir, certFile, keyFile, srv.Bind); err != nil {
				return nil, fmt.Errorf("generate self-signed certificate: %w", err)
			}
			res.Generated = true
		}
	default:
		return nil, fmt.Errorf("unknown tls mode %q", srv.TLS.Mode)
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	sum := sha256.Sum256(cert.Certificate[0])
	res.Fingerprint = colonHex(sum[:])
	res.CertFile = certFile
	res.Config = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	return res, nil
}

func generate(dir, certFile, keyFile, bind string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	dns := []string{"localhost"}
	if hostname != "" {
		dns = append(dns, hostname)
	}
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if ip := net.ParseIP(bind); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		ips = append(ips, ip)
	} else if ip != nil && ip.IsUnspecified() {
		ips = append(ips, localAddresses()...)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "fleetmgr self-signed", Organization: []string{"fleetmgr"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return writeFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// writeFile writes atomically via a temp file and rename.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func localAddresses() []net.IP {
	var out []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP)
		}
	}
	return out
}

func colonHex(b []byte) string {
	h := strings.ToUpper(hex.EncodeToString(b))
	parts := make([]string, 0, len(b))
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}
