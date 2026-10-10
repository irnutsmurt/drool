package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// cmdHealthcheck asks this controller's own /health endpoint whether it is
// up (container health checks: the image has no shell or curl). Exit 0
// healthy, 1 otherwise. It sends no credentials; with TLS it talks to its
// own address, so the self-signed certificate isn't checked.
func cmdHealthcheck(path string, explicit bool) error {
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	host := cfg.Server.Bind
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	scheme := "https"
	if cfg.Server.TLS.Mode == "off" {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/health", scheme, net.JoinHostPort(host, strconv.Itoa(cfg.Server.WebPort)))
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // its own address; nothing secret is sent
	r, err := c.Get(url)
	if err != nil {
		return fmt.Errorf("unhealthy: %w", err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if r.StatusCode != http.StatusOK {
		return errors.New("unhealthy: " + r.Status + ": " + string(body))
	}
	fmt.Println(string(body))
	return nil
}
