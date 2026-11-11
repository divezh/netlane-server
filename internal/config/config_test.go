package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/divezh/netlane-server/internal/security"
)

func TestSetupIsExclusiveAndPublicListenerNeedsTLS(t *testing.T) {
	dir := t.TempDir()
	c, err := New("vpn.example.net", "admin", "long-password-123", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = SaveNew(path, c); err != nil {
		t.Fatal(err)
	}
	if err = SaveNew(path, c); err == nil {
		t.Fatal("setup overwrote existing configuration")
	}
	loaded, err := Load(path)
	if err != nil || loaded.EncryptionKey != c.EncryptionKey {
		t.Fatal("config did not round-trip")
	}
	c.Listen = "0.0.0.0:8443"
	if c.Validate() == nil {
		t.Fatal("public plaintext listener accepted")
	}
	c.Listen = "127.0.0.1:8080"
	c.MetricsAddress = "192.0.2.1:1987"
	if c.Validate() == nil {
		t.Fatal("nonlocal metrics target accepted")
	}
}

func TestSaveReplacesPasswordAtomically(t *testing.T) {
	dir := t.TempDir()
	c, err := New("vpn.example.net", "admin", "long-password-123", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = SaveNew(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Path != path {
		t.Fatalf("load: %v %q", err, loaded.Path)
	}
	if loaded.SetPassword("short") == nil {
		t.Fatal("short admin password accepted")
	}
	if err = loaded.SetPassword("another-long-password"); err != nil {
		t.Fatal(err)
	}
	if err = Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.PasswordHash == c.PasswordHash || again.EncryptionKey != c.EncryptionKey || !security.CheckPassword(again.PasswordHash, "another-long-password") {
		t.Fatal("password was not replaced or other settings changed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	if err = Save(filepath.Join(dir, "missing.json"), loaded); err == nil {
		t.Fatal("Save created a config that did not exist")
	}
}

func TestServerIdentityValidation(t *testing.T) {
	c, err := New("vpn.example.net", "admin", "administrator-password", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range [][2]string{{"", ""}, {"Амстердам", "NL"}, {"Амстердам", ""}, {strings.Repeat("я", 64), "DE"}} {
		c.ServerName, c.ServerCountry = ok[0], ok[1]
		if err := c.Validate(); err != nil {
			t.Fatalf("valid identity %q rejected: %v", ok, err)
		}
	}
	for _, bad := range [][2]string{{"x", "nl1"}, {"x", "N"}, {"x", "nl"}, {strings.Repeat("я", 65), ""}, {"a\nb", ""}, {"a\x07b", ""}} {
		c.ServerName, c.ServerCountry = bad[0], bad[1]
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid identity %q accepted", bad)
		}
	}
}

func TestDecoyListenMustBeLoopback(t *testing.T) {
	c, err := New("vpn.example.net", "admin", "administrator-password", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"", "127.0.0.1:8090"} {
		c.DecoyListen = ok
		if err := c.Validate(); err != nil {
			t.Fatalf("decoy %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:8090", "8090", "example.com:80"} {
		c.DecoyListen = bad
		if c.Validate() == nil {
			t.Fatalf("decoy %q accepted", bad)
		}
	}
}

func TestEnsureSyncPrefix(t *testing.T) {
	var c Config
	if !c.EnsureSyncPrefix() {
		t.Fatal("empty prefix not filled")
	}
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(c.SyncPrefix) {
		t.Fatalf("bad prefix %q", c.SyncPrefix)
	}
	first := c.SyncPrefix
	if c.EnsureSyncPrefix() || c.SyncPrefix != first {
		t.Fatal("existing prefix changed")
	}
}

func TestValidateRejectsBadSyncPrefix(t *testing.T) {
	c, err := New("vpn.example.com", "admin", "correct horse battery", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"abc", "aaaaaaaaaaaaaaaaaaaaaaaaa1"} {
		c.SyncPrefix = bad
		if c.Validate() == nil {
			t.Fatalf("prefix %q accepted", bad)
		}
	}
	c.SyncPrefix = "abcdefghijklmnopqrstuvwxyz"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeConfig(t *testing.T) {
	dir := t.TempDir()
	c, err := NewNode("de.example.com", "https://ams.example.com/p", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsNode() || c.Validate() != nil {
		t.Fatal("node config invalid")
	}
	bad := c
	bad.MainURL = "http://ams.example.com/p"
	if bad.Validate() == nil {
		t.Fatal("plain http main accepted")
	}
	bad = c
	bad.NodeTokenFile = "node.token"
	if bad.Validate() == nil {
		t.Fatal("relative token file accepted")
	}
}

func TestSaveNewConcurrentAtomicPublish(t *testing.T) {
	dir := t.TempDir()
	c, err := New("vpn.example.net", "admin", "long-password-123", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- SaveNew(path, c) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("published %d configs", successes)
	}
	restored, err := Load(path)
	if err != nil || restored.EncryptionKey != c.EncryptionKey || restored.PasswordHash != c.PasswordHash {
		t.Fatal("partial or replaced configuration", err)
	}
}
