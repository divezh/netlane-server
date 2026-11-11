package endpoint

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsAreLocalAndNeverFollowRedirects(t *testing.T) {
	if _, err := NewMetrics("example.net:1987"); err == nil {
		t.Fatal("hostname accepted")
	}
	leaked := false
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer external.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, external.URL, 302) }))
	defer source.Close()
	m, _ := NewMetrics(strings.TrimPrefix(source.URL, "http://"))
	if _, err := m.Clients(context.Background()); err == nil || leaked {
		t.Fatal("metrics redirect followed")
	}
}
func TestClientsAreParsedAndFailureIsNotZero(t *testing.T) {
	status := 200
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`[{"username":"alex","ip":null,"sessions":2,"inbound":10,"outbound":20}]`))
	}))
	defer source.Close()
	m, _ := NewMetrics(strings.TrimPrefix(source.URL, "http://"))
	clients, err := m.Clients(context.Background())
	if err != nil || len(clients) != 1 || clients[0].Sessions != 2 {
		t.Fatalf("parse: %v", err)
	}
	status = 404
	if _, err = m.Clients(context.Background()); err == nil {
		t.Fatal("unavailable metrics reported as zero")
	}
}
