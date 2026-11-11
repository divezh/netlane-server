package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

func savedMain(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.New("vpn.example.com", "admin", "test-admin-password", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = config.SaveNew(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrepareMainAddsSyncPrefixOnce(t *testing.T) {
	path := savedMain(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = prepareMain(&cfg); err != nil {
		t.Fatal(err)
	}
	again, _ := config.Load(path)
	if again.SyncPrefix == "" || again.SyncPrefix != cfg.SyncPrefix {
		t.Fatal("prefix not saved", again.SyncPrefix, cfg.SyncPrefix)
	}
	if err = prepareMain(&again); err != nil {
		t.Fatal(err)
	}
	third, _ := config.Load(path)
	if third.SyncPrefix != cfg.SyncPrefix {
		t.Fatal("prefix changed")
	}
}

func TestNodeCommandRejectsMainConfig(t *testing.T) {
	err := run([]string{"node", "--config", savedMain(t)})
	if err == nil || !strings.Contains(err.Error(), "не нода") {
		t.Fatal(err)
	}
}

func TestServeRejectsNodeConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.NewNode("de.example.com", "https://ams.example.com/p", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = config.SaveNew(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err = run([]string{"serve", "--config", path}); err == nil || !strings.Contains(err.Error(), "конфигурация ноды") {
		t.Fatal(err)
	}
}

// Lost phone and recovery codes: root on the server turns the second factor off.
func TestReset2FA(t *testing.T) {
	path := savedMain(t)
	cfg, _ := config.Load(path)
	key, _ := cfg.Key()
	db, err := store.Open(cfg.Database, key)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ReplaceAdminFactor(context.Background(), "", "sealed-factor"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	db.Close()
	if err = run([]string{"reset-2fa", "--config", path}); err != nil {
		t.Fatal(err)
	}
	db, _ = store.Open(cfg.Database, key)
	defer db.Close()
	if sealed, _ := db.AdminFactor(context.Background()); sealed != "" {
		t.Fatal("second factor still on")
	}
	if err = run([]string{"reset-2fa", "--config", path}); err != nil {
		t.Fatal("reset with 2FA already off must succeed:", err)
	}
}

func TestJoinResumesDurableTokenWithoutReusingInvite(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("a", 43) + "\n"
	path := filepath.Join(dir, "config.json")
	pending, err := config.NewNode("de.example.com", "https://example.invalid/abcdefghijklmnopqrstuvwxyz", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.SaveNew(path+".pending", pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	// example.invalid cannot accept a network join: this must only restore the config.
	if err := joinNode(path, "https://example.invalid/abcdefghijklmnopqrstuvwxyz/node/join#already-used", "de.example.com", "DE", "DE", dir, "test"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || !cfg.IsNode() {
		t.Fatal(cfg, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "node.token"))
	if err != nil || string(data) != token {
		t.Fatal("token changed", err)
	}
}

func TestJoinRejectsTokenForDifferentMain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	token := strings.Repeat("b", 43) + "\n"
	pending, err := config.NewNode("de.example.com", "https://original.invalid/abcdefghijklmnopqrstuvwxyz", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.SaveNew(path+".pending", pending); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "node.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	err = joinNode(path, "https://other.invalid/abcdefghijklmnopqrstuvwxyz/node/join#unused", "de.example.com", "DE", "DE", dir, "test")
	if err == nil || !strings.Contains(err.Error(), "другому") {
		t.Fatal("different main accepted", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "node.token"))
	if string(data) != token {
		t.Fatal("token overwritten")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unexpected final config")
	}
}
