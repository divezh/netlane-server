package qrcode

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDataURI(t *testing.T) {
	uri, err := DataURI("tt://?" + strings.Repeat("A", 700))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/svg+xml;base64,"))
	if err != nil || !strings.HasPrefix(uri, "data:image/svg+xml;base64,") || !strings.HasPrefix(string(raw), "<svg") || !strings.Contains(string(raw), "h1v1h-1z") {
		t.Fatalf("%v %.80s", err, raw)
	}
	if _, err := DataURI(strings.Repeat("x", 4000)); err == nil {
		t.Fatal("text beyond QR capacity accepted")
	}
}
