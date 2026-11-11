package api

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
)

// PayloadHeader carries a node's request data. The VPN endpoint's reverse proxy passes
// headers to the decoy server but drops request bodies, so nodes send JSON here
// (gzip, then base64url without padding).
const PayloadHeader = "X-Sync-Data"

// PollHeader asks main to answer a sync at once instead of holding it (polling mode).
const PollHeader = "X-Sync-Poll"

func EncodePayload(data []byte) (string, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

func DecodePayload(value string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("payload too large")
	}
	return data, nil
}

// readPayload returns the request data from the header or, without one, the body.
func readPayload(r *http.Request, limit int64) ([]byte, error) {
	if value := r.Header.Get(PayloadHeader); value != "" {
		data, err := DecodePayload(value)
		if err == nil && int64(len(data)) > limit {
			return nil, errors.New("payload too large")
		}
		return data, err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, errors.New("payload too large")
	}
	return data, err
}
