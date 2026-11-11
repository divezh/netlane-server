package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	Username string  `json:"username"`
	IP       *string `json:"ip"`
	Sessions uint64  `json:"sessions"`
	Inbound  uint64  `json:"inbound"`
	Outbound uint64  `json:"outbound"`
	// Country is filled by the panel from its local GeoIP database, not by the
	// endpoint: the endpoint reports addresses, the panel decides what they mean.
	Country string `json:"country,omitempty"`
}
type Metrics struct {
	address string
	client  *http.Client
}

func NewMetrics(address string) (*Metrics, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("metrics address must be a loopback IP")
	}
	return &Metrics{address: address, client: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (m *Metrics) Clients(ctx context.Context) ([]Client, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+m.address+"/clients", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("TrustTunnel metrics unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TrustTunnel /clients returned %d; check per_client_metrics", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("metrics response too large")
	}
	var clients []Client
	err = json.Unmarshal(data, &clients)
	if clients == nil {
		clients = []Client{}
	}
	return clients, err
}
