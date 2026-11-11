package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// source is the monthly DB-IP Lite Country archive. The database is optional for the
// panel, but a server that never received it draws no flags for anyone, so the panel
// installs it itself instead of relying on the updater and the systemd timer alone.
const source = "https://download.db-ip.com/free/dbip-country-lite-%s.mmdb.gz"

// Size bounds for the downloaded database: a real file is a few megabytes, so anything
// tiny is an error page and anything huge is not the database we asked for.
const (
	minDatabase = 64 << 10
	maxDatabase = 64 << 20
)

// Ensure installs the country database at path when the file is missing. The current
// month is tried first and the previous one second, because DB-IP publishes a new file
// a few days into the month. An existing file is left alone: geoip-update and its
// monthly timer refresh it.
func Ensure(ctx context.Context, path string, now time.Time) error {
	return ensure(ctx, path, now, fetch)
}

// ensure is Ensure with the download injected, so tests need neither the network nor
// the real DB-IP host.
func ensure(ctx context.Context, path string, now time.Time, download func(context.Context, string, string) error) error {
	switch _, err := os.Stat(path); {
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("geoip: stat %s: %w", path, err)
	}
	months := []string{now.UTC().Format("2006-01"), now.UTC().AddDate(0, -1, 0).Format("2006-01")}
	var failure error
	for _, month := range months {
		if err := download(ctx, fmt.Sprintf(source, month), path); err != nil {
			failure = err
			continue
		}
		return nil
	}
	return failure
}

// fetch downloads one archive and replaces path with its contents. The new file is
// written beside the target and opened before the rename, so a truncated or foreign
// download never becomes the database the panel reads.
func fetch(ctx context.Context, url, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("geoip: %s: %s", url, response.Status)
	}
	archive, err := gzip.NewReader(response.Body)
	if err != nil {
		return fmt.Errorf("geoip: %s: %w", url, err)
	}
	defer archive.Close()

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".geoip-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)

	written, err := io.Copy(temporary, io.LimitReader(archive, maxDatabase+1))
	if err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if written < minDatabase || written > maxDatabase {
		return fmt.Errorf("geoip: %s: неожиданный размер базы: %d байт", url, written)
	}
	db, err := openDatabase(name)
	if err != nil {
		return fmt.Errorf("geoip: скачанная база не читается: %w", err)
	}
	db.Close()
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
