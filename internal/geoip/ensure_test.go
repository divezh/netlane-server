package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureLeavesExistingDatabaseAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	if err := os.WriteFile(path, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}
	downloads := 0
	err := ensure(context.Background(), path, time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), func(context.Context, string, string) error {
		downloads++
		return nil
	})
	if err != nil {
		t.Fatalf("an existing database must not be an error, got %v", err)
	}
	if downloads != 0 {
		t.Fatalf("an existing database must not be downloaded, downloads: %d", downloads)
	}
}

// Ensure is the entry point used at startup; with the file in place it must not reach
// the network at all.
func TestEnsureWithExistingFileSkipsTheNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	if err := os.WriteFile(path, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), path, time.Now()); err != nil {
		t.Fatalf("Ensure must accept an existing database, got %v", err)
	}
}

func TestEnsureTriesThePreviousMonth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	at := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	var urls []string
	err := ensure(context.Background(), path, at, func(_ context.Context, url, target string) error {
		urls = append(urls, url)
		if len(urls) == 1 {
			return errors.New("ещё не опубликована")
		}
		return os.WriteFile(target, []byte("database"), 0o644)
	})
	if err != nil {
		t.Fatalf("the previous month must be tried when the current one fails, got %v", err)
	}
	if len(urls) != 2 || !strings.Contains(urls[0], "2026-11") || !strings.Contains(urls[1], "2026-10") {
		t.Fatalf("unexpected download order: %v", urls)
	}
}

func TestEnsureReportsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	failure := errors.New("сеть недоступна")
	err := ensure(context.Background(), path, time.Now(), func(context.Context, string, string) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("a failed download must be reported, got %v", err)
	}
}

func TestFetchRejectsTinyDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		archive := gzip.NewWriter(w)
		_, _ = archive.Write([]byte("это не база"))
		_ = archive.Close()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	if err := fetch(context.Background(), server.URL, path); err == nil {
		t.Fatal("a download without a database must be rejected")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected download must not leave a database, got %v", err)
	}
}

func TestFetchReportsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "нет такой базы", http.StatusNotFound)
	}))
	defer server.Close()
	if err := fetch(context.Background(), server.URL, filepath.Join(t.TempDir(), "geoip.mmdb")); err == nil {
		t.Fatal("a non-200 answer must be an error")
	}
}
