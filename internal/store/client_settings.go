package store

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

var ErrClientSettings = errors.New("invalid client settings")
var ErrSettingsConflict = errors.New("client settings revision conflict")

type SharedAppRule struct {
	Name   string `json:"Name"`
	Action string `json:"Action"`
}
type SharedRouting struct {
	Mode         string          `json:"Mode"`
	Destinations []string        `json:"Destinations"`
	Applications []SharedAppRule `json:"Applications"`
}
type SharedRuleSet struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Routing SharedRouting `json:"routing"`
}
type ClientSettings struct {
	Version    int             `json:"version"`
	Revision   int64           `json:"revision"`
	DNSServers []string        `json:"dns_servers"`
	RuleSets   []SharedRuleSet `json:"rule_sets"`
}

var sharedDomain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)
var sharedID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func publicDNSIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

func validSharedDNS(value string) bool {
	if len(value) == 0 || len(value) > 512 || strings.HasSuffix(value, ":") || strings.ContainsAny(value, "\\%") || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	if ip, err := netip.ParseAddr(value); err == nil {
		return publicDNSIP(ip)
	}
	if address, err := netip.ParseAddrPort(value); err == nil {
		return address.Port() > 0 && publicDNSIP(address.Addr())
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme != "tls" && u.Scheme != "https" && u.Scheme != "tcp" && u.Scheme != "quic" {
		return false
	}
	if u.Port() != "" {
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if !publicDNSIP(ip) {
			return false
		}
	} else if !sharedDomain.MatchString(strings.ToLower(u.Hostname())) || strings.Trim(u.Hostname(), "0123456789.") == "" || len(u.Hostname()) > 253 {
		return false
	}
	for _, label := range strings.Split(u.Hostname(), ".") {
		if len(label) > 63 {
			return false
		}
	}
	return u.Scheme == "https" || ((u.Path == "" || u.Path == "/") && u.RawQuery == "")
}

// canonicalSharedDNS matches the host/port spelling the Windows client keeps.
func canonicalSharedDNS(value string) string {
	if ip, err := netip.ParseAddr(value); err == nil {
		return ip.String()
	}
	if address, err := netip.ParseAddrPort(value); err == nil {
		return address.String()
	}
	u, err := url.Parse(value)
	if err != nil {
		return value
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	}
	port := u.Port()
	if u.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host
	if u.Scheme == "https" {
		if u.Path == "" {
			u.Path = "/"
		}
	} else {
		u.Path = ""
	}
	return u.String()
}

func (settings *ClientSettings) Validate() error {
	if settings.Version != 1 || settings.Revision < 0 || len(settings.RuleSets) > 10 || len(settings.DNSServers) > 8 {
		return ErrClientSettings
	}
	seen := map[string]bool{}
	for i, dns := range settings.DNSServers {
		if !validSharedDNS(dns) {
			return ErrClientSettings
		}
		dns = canonicalSharedDNS(dns)
		if seen[dns] {
			return ErrClientSettings
		}
		seen[dns] = true
		settings.DNSServers[i] = dns
	}
	seen = map[string]bool{}
	names := map[string]bool{}
	for i := range settings.RuleSets {
		set := &settings.RuleSets[i]
		set.Name = strings.TrimSpace(set.Name)
		name := strings.ToLower(set.Name)
		if !sharedID.MatchString(set.ID) || seen[set.ID] || len(utf16.Encode([]rune(set.Name))) < 1 || len(utf16.Encode([]rune(set.Name))) > 40 || strings.IndexFunc(set.Name, unicode.IsControl) >= 0 || names[name] {
			return ErrClientSettings
		}
		seen[set.ID] = true
		names[name] = true
		if set.Routing.Mode != "general" && set.Routing.Mode != "selective" {
			return ErrClientSettings
		}
		if len(set.Routing.Destinations) > 256 || len(set.Routing.Applications) > 100 {
			return ErrClientSettings
		}
		targets := map[string]bool{}
		for j, value := range set.Routing.Destinations {
			value = strings.ToLower(strings.TrimSpace(value))
			if len(value) > 260 || targets[value] {
				return ErrClientSettings
			}
			valid := false
			if ip, err := netip.ParseAddr(value); err == nil {
				valid = ip.Is4()
			} else if prefix, err := netip.ParsePrefix(value); err == nil {
				valid = prefix.Addr().Is4()
			} else {
				domain := strings.TrimPrefix(value, "*.")
				valid = len(domain) <= 253 && sharedDomain.MatchString(domain)
				for _, label := range strings.Split(domain, ".") {
					if len(label) > 63 {
						valid = false
					}
				}
				if strings.Trim(domain, "0123456789.") == "" {
					valid = false
				}
			}
			if !valid {
				return ErrClientSettings
			}
			targets[value] = true
			set.Routing.Destinations[j] = value
		}
		apps := map[string]bool{}
		for _, app := range set.Routing.Applications {
			name := strings.ToLower(app.Name)
			if len(app.Name) < 5 || len(app.Name) > 255 || !strings.HasSuffix(name, ".exe") || strings.ContainsAny(app.Name, "\\/:*?\"<>|") || strings.IndexFunc(app.Name, unicode.IsControl) >= 0 || apps[name] || (app.Action != "vpn" && app.Action != "direct") {
				return ErrClientSettings
			}
			apps[name] = true
		}
		if set.Routing.Destinations == nil {
			set.Routing.Destinations = []string{}
		}
		if set.Routing.Applications == nil {
			set.Routing.Applications = []SharedAppRule{}
		}
	}
	if settings.RuleSets == nil {
		settings.RuleSets = []SharedRuleSet{}
	}
	data, err := json.Marshal(settings)
	if err != nil || len(data) > 128*1024 {
		return ErrClientSettings
	}
	return nil
}

func (s *Store) ClientSettings(ctx context.Context) (ClientSettings, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT settings FROM client_settings WHERE id=1").Scan(&data)
	var settings ClientSettings
	if err == nil {
		err = json.Unmarshal([]byte(data), &settings)
	}
	return settings, err
}
func (s *Store) SaveClientSettings(ctx context.Context, actor string, settings ClientSettings) (ClientSettings, error) {
	if err := settings.Validate(); err != nil {
		return settings, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return settings, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM client_settings WHERE id=1").Scan(&revision); err != nil {
		return settings, err
	}
	if revision != settings.Revision {
		return settings, ErrSettingsConflict
	}
	settings.Revision++
	data, err := json.Marshal(settings)
	if err != nil {
		return settings, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE client_settings SET revision=?,settings=? WHERE id=1 AND revision=?", settings.Revision, string(data), revision)
	if err != nil {
		return settings, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return settings, ErrSettingsConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(strftime('%Y-%m-%dT%H:%M:%SZ','now'),?,'subscription.settings_updated','all')", actor); err != nil {
		return settings, err
	}
	return settings, tx.Commit()
}
