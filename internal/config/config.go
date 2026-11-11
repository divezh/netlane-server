package config

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/divezh/netlane-server/internal/security"
)

type Config struct {
	Version        int    `json:"version"`
	Domain         string `json:"domain"`
	Listen         string `json:"listen"`
	Database       string `json:"database"`
	Admin          string `json:"admin"`
	PasswordHash   string `json:"password_hash"`
	EncryptionKey  string `json:"encryption_key"`
	MetricsAddress string `json:"metrics_address"`
	ControlSocket  string `json:"control_socket,omitempty"`
	TLSCert        string `json:"tls_cert,omitempty"`
	TLSKey         string `json:"tls_key,omitempty"`
	// ServerName and ServerCountry (ISO 3166-1 alpha-2) name this server in issued profiles.
	ServerName    string `json:"server_name,omitempty"`
	ServerCountry string `json:"server_country,omitempty"`
	// GeoIPPath is the country database used for client flags; empty means geoip.mmdb
	// next to the panel database.
	GeoIPPath string `json:"geoip_path,omitempty"`
	// DecoyListen is the loopback address of the ordinary website the VPN endpoint shows to
	// requests without VPN credentials (its reverse_proxy); empty turns it off.
	DecoyListen string `json:"decoy_listen,omitempty"`
	// SyncPrefix is the secret first path segment under which the decoy server answers nodes
	// and subscriptions (main only).
	SyncPrefix string `json:"sync_prefix,omitempty"`
	// Role is "node" for a location that syncs users from main; empty or "main" otherwise.
	Role          string `json:"role,omitempty"`
	MainURL       string `json:"main_url,omitempty"`
	NodeTokenFile string `json:"node_token_file,omitempty"`
	NodeStateFile string `json:"node_state_file,omitempty"`
	// Path is the file the config was loaded from; it is not stored.
	Path string `json:"-"`
}

func New(domain, admin, password, dir string) (Config, error) {
	hash, err := security.HashPassword(password)
	if err != nil {
		return Config{}, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	c := Config{Version: 1, Domain: domain, Listen: "127.0.0.1:8080", Database: filepath.Join(abs, "panel.sqlite"), Admin: admin, PasswordHash: hash, EncryptionKey: security.Token(32), MetricsAddress: "127.0.0.1:1987"}
	return c, c.Validate()
}

// NewNode is the configuration of a node: the VPN domain, main's sync URL and a data dir.
func NewNode(domain, mainURL, dir string) (Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	c := Config{Version: 1, Domain: domain, Role: "node", MainURL: mainURL, ControlSocket: "/run/trusttunnel/control.sock", MetricsAddress: "127.0.0.1:1987", DecoyListen: "127.0.0.1:8090", NodeTokenFile: filepath.Join(abs, "node.token"), NodeStateFile: filepath.Join(abs, "node.state")}
	return c, c.Validate()
}

func (c Config) IsNode() bool { return c.Role == "node" }

// GeoIPFile is the MaxMind-format country database used to draw client flags. The
// file is optional: without it the panel works and simply shows no flags. By default
// it sits next to the panel database, where scripts/geoip-update.sh installs it.
func (c Config) GeoIPFile() string {
	if c.GeoIPPath != "" {
		return c.GeoIPPath
	}
	return filepath.Join(filepath.Dir(c.Database), "geoip.mmdb")
}

var syncPrefix = regexp.MustCompile(`^[a-z2-7]{26}$`)

// EnsureSyncPrefix creates the secret sync prefix when it is missing; it reports a change.
func (c *Config) EnsureSyncPrefix() bool {
	if c.SyncPrefix != "" {
		return false
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	c.SyncPrefix = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	return true
}

func (c Config) Key() ([]byte, error) { return base64.RawURLEncoding.DecodeString(c.EncryptionKey) }
func (c Config) Validate() error {
	if c.ControlSocket != "" && !filepath.IsAbs(c.ControlSocket) {
		return errors.New("control socket path must be absolute")
	}
	if err := ValidateServerIdentity(c.ServerName, c.ServerCountry); err != nil {
		return err
	}
	if c.Version != 1 {
		return errors.New("unsupported config version")
	}
	if c.SyncPrefix != "" && !syncPrefix.MatchString(c.SyncPrefix) {
		return errors.New("invalid sync prefix")
	}
	if c.Role != "" && c.Role != "main" && c.Role != "node" {
		return errors.New("invalid role")
	}
	if c.IsNode() {
		return c.validateNode()
	}
	if strings.TrimSpace(c.Admin) == "" || len(c.Admin) > 64 {
		return errors.New("invalid admin login")
	}
	if c.Domain == "" || strings.ContainsAny(c.Domain, " /\\\r\n:@") {
		return errors.New("invalid domain")
	}
	if !filepath.IsAbs(c.Database) {
		return errors.New("database path must be absolute")
	}
	key, err := c.Key()
	if err != nil || len(key) != 32 {
		return errors.New("invalid encryption key")
	}
	if !strings.HasPrefix(c.PasswordHash, "argon2id-v1$") {
		return errors.New("invalid password hash")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("TLS requires both certificate and private key")
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && c.TLSCert == "" {
		return errors.New("public listener requires TLS")
	}
	if c.DecoyListen != "" {
		host, _, err := net.SplitHostPort(c.DecoyListen)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("decoy site must use a loopback IP")
		}
	}
	host, _, err = net.SplitHostPort(c.MetricsAddress)
	if err != nil {
		return fmt.Errorf("metrics address: %w", err)
	}
	ip = net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("metrics must use a loopback IP")
	}
	return nil
}

func (c Config) validateNode() error {
	if c.Domain == "" || strings.ContainsAny(c.Domain, " /\\\r\n:@") {
		return errors.New("invalid domain")
	}
	u, err := url.Parse(c.MainURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("main URL must be https://")
	}
	if !filepath.IsAbs(c.NodeTokenFile) || !filepath.IsAbs(c.NodeStateFile) || !filepath.IsAbs(c.ControlSocket) {
		return errors.New("node token and state paths must be absolute")
	}
	for _, address := range []string{c.DecoyListen, c.MetricsAddress} {
		host, _, err := net.SplitHostPort(address)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("decoy and metrics must use a loopback IP")
		}
	}
	return nil
}

// ValidateServerIdentity accepts an empty or printable name of up to 64 characters and an
// empty or two-letter upper-case country code.
func ValidateServerIdentity(name, country string) error {
	if utf8.RuneCountInString(name) > 64 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return errors.New("invalid server name")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("invalid server name")
		}
	}
	if country != "" && (len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z') {
		return errors.New("invalid server country")
	}
	return nil
}
func Load(path string) (Config, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return Config{}, err
	}
	if runtime.GOOS != "windows" && stat.Mode().Perm()&0077 != 0 {
		return Config{}, errors.New("config must not be accessible to group or other users (chmod 600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	c.Path = path
	return c, c.Validate()
}

// Save atomically replaces an existing config: a private temporary file in the same
// directory is synced, given the original owner and renamed over the old file.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(temporary)
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = keepOwner(f, stat); err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	ok = true
	return syncDir(dir)
}

// SetPassword validates a new administrator password and stores its Argon2id hash.
func (c *Config) SetPassword(password string) error {
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	c.PasswordHash = hash
	return nil
}

// SaveNew publishes a complete, synced config without overwriting an existing one.
// An interrupted write can leave a private temp file, never a truncated config.json.
func SaveNew(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-new-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(c); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Link is atomic and fails if another setup already created the final config.
	if err = os.Link(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}
