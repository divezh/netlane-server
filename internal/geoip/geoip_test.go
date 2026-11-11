package geoip

import (
	"net"
	"path/filepath"
	"testing"
)

type stubReader struct {
	code string
	err  error
	seen net.IP
}

func (s *stubReader) Lookup(ip net.IP, result any) error {
	s.seen = ip
	if s.err != nil {
		return s.err
	}
	target, ok := result.(*record)
	if !ok {
		return nil
	}
	target.Country.ISOCode = s.code
	return nil
}
func (s *stubReader) Close() error { return nil }

func TestOpenMissingFileIsNotAnError(t *testing.T) {
	lookup, err := Open(filepath.Join(t.TempDir(), "geoip.mmdb"))
	if err != nil {
		t.Fatalf("Open on a missing file must succeed, got %v", err)
	}
	if lookup.Available() {
		t.Fatal("a lookup without a database must not report itself as available")
	}
	if code := lookup.Country("203.0.113.10"); code != "" {
		t.Fatalf("no database must yield no country, got %q", code)
	}
}

func TestCountryFromStub(t *testing.T) {
	lookup := &Lookup{db: &stubReader{code: "PL"}}
	if code := lookup.Country("203.0.113.10"); code != "PL" {
		t.Fatalf("expected PL, got %q", code)
	}
}

func TestCountryRejectsBadInput(t *testing.T) {
	stub := &stubReader{code: "PL"}
	lookup := &Lookup{db: stub}
	for _, address := range []string{"", "not-an-ip", "203.0.113.10:443"} {
		if code := lookup.Country(address); code != "" {
			t.Fatalf("address %q must yield no country, got %q", address, code)
		}
	}
	if stub.seen != nil {
		t.Fatalf("a malformed address must not reach the database, got %v", stub.seen)
	}
}

func TestNilLookupIsUsable(t *testing.T) {
	var lookup *Lookup
	if lookup.Available() {
		t.Fatal("a nil lookup is not available")
	}
	if code := lookup.Country("203.0.113.10"); code != "" {
		t.Fatalf("a nil lookup must yield no country, got %q", code)
	}
	if err := lookup.Close(); err != nil {
		t.Fatalf("closing a nil lookup must be a no-op, got %v", err)
	}
}

func TestLookupFailureYieldsNoCountry(t *testing.T) {
	lookup := &Lookup{db: &stubReader{err: net.ErrClosed}}
	if code := lookup.Country("203.0.113.10"); code != "" {
		t.Fatalf("a failed lookup must yield no country, got %q", code)
	}
}
