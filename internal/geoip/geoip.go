// Package geoip resolves a client address to an ISO 3166-1 alpha-2 country code
// using a local MaxMind-format database. The panel ships and reads DB-IP Lite
// Country (CC BY 4.0), so client addresses never leave the server.
//
// The database is optional. When the file is missing, Open returns a lookup that
// answers with an empty code and the panel simply draws no flag; an unreadable
// or corrupt file is reported as an error, because that is a broken installation
// rather than a choice.
package geoip

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
)

// Lookup answers country queries from an open database. The zero value is a
// working lookup that knows nothing, and a nil *Lookup is safe to use.
type Lookup struct {
	db reader
}

// reader is the part of maxminddb.Reader this package needs; keeping it narrow
// lets tests substitute a stub.
type reader interface {
	Lookup(ip net.IP, result any) error
	Close() error
}

// record mirrors the country object of the database.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

// Open reads the database at path. A missing file yields an empty lookup and no
// error: the panel works without flags until the updater installs the database.
func Open(path string) (*Lookup, error) {
	db, err := openDatabase(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Lookup{}, nil
		}
		return nil, fmt.Errorf("geoip: open %s: %w", path, err)
	}
	return &Lookup{db: db}, nil
}

// Country returns the alpha-2 code for address, or an empty string when the
// address is empty, malformed, missing from the database or the database is absent.
func (l *Lookup) Country(address string) string {
	if l == nil || l.db == nil || address == "" {
		return ""
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return ""
	}
	var found record
	if err := l.db.Lookup(ip, &found); err != nil {
		return ""
	}
	return found.Country.ISOCode
}

// Close releases the database.
func (l *Lookup) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	return l.db.Close()
}

// Available reports whether a database is loaded.
func (l *Lookup) Available() bool {
	return l != nil && l.db != nil
}
