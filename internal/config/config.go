// Package config loads controller configuration from built-in defaults, an
// optional YAML file and DROOL_* environment variables, in that order of
// precedence, and records where every effective value came from.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Source identifies which layer supplied a configuration value.
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
	SourceEnv     Source = "env"
)

// EnvPrefix is prepended to the upper-snake-case key path to form the
// environment variable name, e.g. server.web_port → DROOL_SERVER_WEB_PORT.
const EnvPrefix = "DROOL_"

// DefaultPath is used when no --config flag is given and the file exists.
const DefaultPath = "/etc/drool/config.yaml"

type Config struct {
	Server    Server    `yaml:"server"`
	DataDir   string    `yaml:"data_dir"`
	Auth      Auth      `yaml:"auth"`
	Setup     Setup     `yaml:"setup"`
	Secrets   Secrets   `yaml:"secrets"`
	Engine    Engine    `yaml:"engine"`
	Poller    Poller    `yaml:"poller"`
	Retention Retention `yaml:"retention"`
	Logging   Logging   `yaml:"logging"`
	Agent     Agent     `yaml:"agent"`
	Network   Network   `yaml:"network"`
	SSHCA     SSHCA     `yaml:"ssh_ca"`
	Backup    Backup    `yaml:"backup"`
}

// Backup configures backups DROOL makes by itself (M16).
type Backup struct {
	// BeforeUpgrade: before a newer DROOL migrates the database, write a
	// backup to <data_dir>/backups (the newest three are kept). An upgrade
	// that can't make one doesn't start.
	BeforeUpgrade bool `yaml:"before_upgrade"`
}

// SSHCA configures the certificates DROOL's SSH user certificate authority
// issues for hosts in certificate mode (M16).
type SSHCA struct {
	// CertTTL: how long a certificate is valid after it is issued. It is
	// used for one handshake right away; a short life limits what a leaked
	// certificate is worth.
	CertTTL time.Duration `yaml:"cert_ttl"`
	// ClockSkew: certificates are valid from this long before the
	// controller's time, so hosts whose clock is a little behind accept
	// them.
	ClockSkew time.Duration `yaml:"clock_skew"`
}

// Network configures network connectivity monitoring (M10).
type Network struct {
	// GeoIPDir holds optional MaxMind-format (.mmdb) country and ASN
	// databases the operator installs and updates (default
	// <data_dir>/geoip). DROOL only reads them; it never downloads them or
	// looks addresses up online.
	GeoIPDir string `yaml:"geoip_dir"`
}

// GeoIPDir returns the GeoIP database directory.
func (c *Config) GeoIPDir() string {
	if c.Network.GeoIPDir != "" {
		return c.Network.GeoIPDir
	}
	return filepath.Join(c.DataDir, "geoip")
}

// Agent configures the agent endpoint (ARCHITECTURE-ALPHA §23).
type Agent struct {
	// Enabled starts the gRPC agent endpoint.
	Enabled bool `yaml:"enabled"`
	// Port is the agent endpoint's listen port (DESIGN §22.2: 9443).
	Port int `yaml:"port"`
	// Bind is the agent endpoint's listen address; empty: server.bind.
	Bind string `yaml:"bind"`
	// Advertise is the address agents connect to (host or host:port),
	// put into enrollment tokens. Empty: the host name the administrator
	// uses for the web UI, with Port.
	Advertise []string `yaml:"advertise"`
	// CertLifetime of agent operational certificates (DESIGN §6.4: 30 days).
	CertLifetime time.Duration `yaml:"cert_lifetime"`
	// RecoveryGrace is how long after expiry a certificate can still be
	// recovered with proof of the identity key (DESIGN §6.4: 180 days).
	RecoveryGrace time.Duration `yaml:"recovery_grace"`
	// TokenTTL is the default lifetime of enrollment tokens.
	TokenTTL time.Duration `yaml:"token_ttl"`
	// BinariesDir holds drool-agent-linux-<arch> binaries served to new
	// hosts. Empty: next to the drool binary, or ../lib/drool from it
	// (the native packages install them in /usr/lib/drool).
	BinariesDir string `yaml:"binaries_dir"`
}

type Server struct {
	Bind                   string   `yaml:"bind"`
	WebPort                int      `yaml:"web_port"`
	TLS                    TLS      `yaml:"tls"`
	TrustedProxies         []string `yaml:"trusted_proxies"`
	AllowInsecurePlaintext bool     `yaml:"allow_insecure_plaintext"`
}

type TLS struct {
	Mode     string `yaml:"mode"` // self_signed | provided | off
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type Auth struct {
	SessionIdleTimeout     time.Duration `yaml:"session_idle_timeout"`
	SessionAbsoluteTimeout time.Duration `yaml:"session_absolute_timeout"`
	// ReauthWindow: how long confirming your password (and code) again
	// lasts for high-impact actions (M16).
	ReauthWindow time.Duration `yaml:"reauth_window"`
}

// Setup controls first-admin bootstrap (ARCHITECTURE-ALPHA §12.5).
type Setup struct {
	// CodeTTL is the lifetime of generated setup codes.
	CodeTTL time.Duration `yaml:"code_ttl"`
	// Secret is an operator-provided setup code for automated deployments.
	// Prefer SecretFile (e.g. a Docker secret) over putting it in the environment.
	Secret     string `yaml:"secret"`
	SecretFile string `yaml:"secret_file"`
}

// Secrets controls encryption of stored credentials.
type Secrets struct {
	// MasterKeyFile defaults to <data_dir>/master.key and is created on first start.
	MasterKeyFile string `yaml:"master_key_file"`
}

// MasterKeyPath returns the effective master key file path.
func (c Config) MasterKeyPath() string {
	if c.Secrets.MasterKeyFile != "" {
		return c.Secrets.MasterKeyFile
	}
	return filepath.Join(c.DataDir, "master.key")
}

type Engine struct {
	GlobalConcurrency      int   `yaml:"global_concurrency"`
	PerJumpHostConcurrency int   `yaml:"per_jump_host_concurrency"`
	OutputCapBytes         int64 `yaml:"output_cap_bytes"`
}

type Poller struct {
	StatusInterval  time.Duration `yaml:"status_interval"`
	UpdatesInterval time.Duration `yaml:"updates_interval"`
	Workers         int           `yaml:"workers"`
}

type Retention struct {
	JobsDays int `yaml:"jobs_days"`
}

type Logging struct {
	Level    string   `yaml:"level"`  // debug | info | warn | error
	Format   string   `yaml:"format"` // json | text
	Output   string   `yaml:"output"` // file | stdout
	Rotation Rotation `yaml:"rotation"`
}

type Rotation struct {
	MaxSizeMB   int  `yaml:"max_size_mb"`
	RetainFiles int  `yaml:"retain_files"`
	Compress    bool `yaml:"compress"`
	RetainDays  int  `yaml:"retain_days"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Server: Server{
			Bind:    "0.0.0.0",
			WebPort: 8080,
			TLS:     TLS{Mode: "self_signed"},
		},
		DataDir: "/var/lib/drool",
		Auth: Auth{
			SessionIdleTimeout:     12 * time.Hour,
			SessionAbsoluteTimeout: 7 * 24 * time.Hour,
			ReauthWindow:           5 * time.Minute,
		},
		Setup:  Setup{CodeTTL: time.Hour},
		SSHCA:  SSHCA{CertTTL: 10 * time.Minute, ClockSkew: 5 * time.Minute},
		Backup: Backup{BeforeUpgrade: true},
		Engine: Engine{
			GlobalConcurrency:      20,
			PerJumpHostConcurrency: 10,
			OutputCapBytes:         10 << 20,
		},
		Poller: Poller{
			StatusInterval:  5 * time.Minute,
			UpdatesInterval: 6 * time.Hour,
			Workers:         4,
		},
		Retention: Retention{JobsDays: 30},
		Agent: Agent{
			Enabled:       true,
			Port:          9443,
			CertLifetime:  30 * 24 * time.Hour,
			RecoveryGrace: 180 * 24 * time.Hour,
			TokenTTL:      time.Hour,
		},
		Logging: Logging{
			Level:  "info",
			Format: "json",
			Output: "file",
			Rotation: Rotation{
				MaxSizeMB:   25,
				RetainFiles: 10,
				Compress:    true,
				RetainDays:  30,
			},
		},
	}
}

// Value describes one effective configuration value for display.
type Value struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source Source `json:"source"`
	EnvVar string `json:"env_var"`
}

// Loaded is a configuration together with the origin of each value.
type Loaded struct {
	Config
	FilePath string
	Sources  map[string]Source
}

// Load builds the effective configuration. path may be empty, in which case
// DefaultPath is used if it exists. environ is normally os.Environ().
func Load(path string, explicit bool, environ []string) (*Loaded, error) {
	cfg := Defaults()
	l := &Loaded{Sources: map[string]Source{}}
	for _, k := range leafKeys(reflect.TypeOf(cfg), "") {
		l.Sources[k] = SourceDefault
	}

	if path == "" {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := applyFile(&cfg, data, l.Sources); err != nil {
			return nil, fmt.Errorf("config file %s: %w", path, err)
		}
		l.FilePath = path
	case errors.Is(err, os.ErrNotExist) && !explicit:
		// No file is fine when the default path was implied.
	default:
		return nil, fmt.Errorf("config file: %w", err)
	}

	if err := applyEnv(&cfg, environ, l.Sources); err != nil {
		return nil, err
	}
	l.Config = cfg
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return l, nil
}

func applyFile(cfg *Config, data []byte, sources map[string]Source) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	markPresent(raw, "", sources)
	return nil
}

func markPresent(m map[string]any, prefix string, sources map[string]Source) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			markPresent(sub, key, sources)
			continue
		}
		if _, known := sources[key]; known {
			sources[key] = SourceFile
		}
	}
}

func applyEnv(cfg *Config, environ []string, sources map[string]Source) error {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, EnvPrefix) {
			env[k] = v
		}
	}
	root := reflect.ValueOf(cfg).Elem()
	for _, key := range leafKeys(root.Type(), "") {
		raw, ok := env[EnvName(key)]
		if !ok {
			continue
		}
		if err := setFromString(fieldByKey(root, key), raw); err != nil {
			return fmt.Errorf("%s: %w", EnvName(key), err)
		}
		sources[key] = SourceEnv
	}
	return nil
}

// EnvName returns the environment variable that overrides key.
func EnvName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

var durationType = reflect.TypeOf(time.Duration(0))

func leafKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		if f.Type.Kind() == reflect.Struct && f.Type != durationType {
			keys = append(keys, leafKeys(f.Type, key)...)
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

func fieldByKey(v reflect.Value, key string) reflect.Value {
	for _, part := range strings.Split(key, ".") {
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0] == part {
				v = v.Field(i)
				break
			}
		}
	}
	return v
}

func setFromString(v reflect.Value, s string) error {
	if v.Type() == durationType {
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		v.SetFloat(f)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Slice:
		var items []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				items = append(items, p)
			}
		}
		v.Set(reflect.ValueOf(items))
	default:
		return fmt.Errorf("unsupported kind %s", v.Kind())
	}
	return nil
}

// Values lists every effective value with its source, sorted by key order of
// the struct definition.
func (l *Loaded) Values() []Value {
	root := reflect.ValueOf(l.Config)
	var out []Value
	for _, key := range leafKeys(root.Type(), "") {
		fv := fieldByKey(root, key)
		val := fv.Interface()
		if d, ok := val.(time.Duration); ok {
			val = d.String()
		}
		if s, ok := val.(string); ok && s != "" && isSecretKey(key) {
			val = "«redacted»"
		}
		out = append(out, Value{Key: key, Value: val, Source: l.Sources[key], EnvVar: EnvName(key)})
	}
	return out
}

// isSecretKey reports whether a config key holds a credential that must not
// be displayed.
func isSecretKey(key string) bool {
	return strings.HasSuffix(key, ".secret") || strings.HasSuffix(key, "_password")
}

// ProvidedSetupSecret returns the operator-provided setup secret from
// setup.secret or setup.secret_file ("" if neither is set).
func (c Config) ProvidedSetupSecret() (string, error) {
	if c.Setup.Secret != "" {
		return strings.TrimSpace(c.Setup.Secret), nil
	}
	if c.Setup.SecretFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.Setup.SecretFile)
	if err != nil {
		return "", fmt.Errorf("setup.secret_file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Validate checks semantic constraints, including the plaintext listener
// rule (ARCHITECTURE-ALPHA decision D7).
func (c Config) Validate() error {
	var errs []error
	switch c.Server.TLS.Mode {
	case "self_signed":
	case "provided":
		if c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "" {
			errs = append(errs, errors.New("server.tls.mode=provided requires server.tls.cert_file and server.tls.key_file"))
		}
	case "off":
		if !IsLoopback(c.Server.Bind) && !c.Server.AllowInsecurePlaintext {
			errs = append(errs, fmt.Errorf(
				"server.tls.mode=off requires a loopback server.bind (got %q); "+
					"trusted_proxies does not make a non-loopback plaintext listener safe. "+
					"Set server.allow_insecure_plaintext=true only if you accept credentials crossing the network unencrypted",
				c.Server.Bind))
		}
	default:
		errs = append(errs, fmt.Errorf("server.tls.mode must be self_signed, provided or off (got %q)", c.Server.TLS.Mode))
	}
	if c.Agent.Port < 1 || c.Agent.Port > 65535 || c.Agent.Port == c.Server.WebPort {
		errs = append(errs, fmt.Errorf("agent.port must be 1-65535 and differ from server.web_port: %d", c.Agent.Port))
	}
	if c.Agent.CertLifetime < time.Hour || c.Agent.RecoveryGrace < 0 || c.Agent.TokenTTL < time.Minute {
		errs = append(errs, fmt.Errorf("agent.cert_lifetime must be at least 1h, agent.recovery_grace not negative, agent.token_ttl at least 1m"))
	}
	if c.Server.WebPort < 1 || c.Server.WebPort > 65535 {
		errs = append(errs, fmt.Errorf("server.web_port out of range: %d", c.Server.WebPort))
	}
	for _, p := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil && net.ParseIP(p) == nil {
			errs = append(errs, fmt.Errorf("server.trusted_proxies: invalid address or CIDR %q", p))
		}
	}
	if c.DataDir == "" || !filepath.IsAbs(c.DataDir) {
		errs = append(errs, fmt.Errorf("data_dir must be an absolute path (got %q)", c.DataDir))
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("logging.level must be debug, info, warn or error (got %q)", c.Logging.Level))
	}
	switch c.Logging.Format {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("logging.format must be json or text (got %q)", c.Logging.Format))
	}
	switch c.Logging.Output {
	case "file", "stdout":
	default:
		errs = append(errs, fmt.Errorf("logging.output must be file or stdout (got %q)", c.Logging.Output))
	}
	if c.Setup.CodeTTL < time.Minute || c.Setup.CodeTTL > 24*time.Hour {
		errs = append(errs, fmt.Errorf("setup.code_ttl must be between 1m and 24h (got %s)", c.Setup.CodeTTL))
	}
	if c.Setup.Secret != "" && c.Setup.SecretFile != "" {
		errs = append(errs, errors.New("set only one of setup.secret and setup.secret_file"))
	}
	if c.Auth.SessionIdleTimeout <= 0 || c.Auth.SessionAbsoluteTimeout < c.Auth.SessionIdleTimeout {
		errs = append(errs, errors.New("auth: session timeouts must be positive and absolute >= idle"))
	}
	if c.SSHCA.CertTTL < time.Minute || c.SSHCA.CertTTL > time.Hour {
		errs = append(errs, fmt.Errorf("ssh_ca.cert_ttl must be between 1m and 1h (got %s)", c.SSHCA.CertTTL))
	}
	if c.SSHCA.ClockSkew < 0 || c.SSHCA.ClockSkew > 30*time.Minute {
		errs = append(errs, fmt.Errorf("ssh_ca.clock_skew must be between 0 and 30m (got %s)", c.SSHCA.ClockSkew))
	}
	if c.Auth.ReauthWindow < time.Minute || c.Auth.ReauthWindow > time.Hour {
		errs = append(errs, fmt.Errorf("auth.reauth_window must be between 1m and 1h (got %s)", c.Auth.ReauthWindow))
	}
	return errors.Join(errs...)
}

// IsInsecurePlaintext reports whether the server will serve plaintext HTTP on
// a non-loopback address (allowed only with explicit opt-in).
func (c Config) IsInsecurePlaintext() bool {
	return c.Server.TLS.Mode == "off" && !IsLoopback(c.Server.Bind)
}

// IsLoopback reports whether bind refers only to the loopback interface.
func IsLoopback(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}
