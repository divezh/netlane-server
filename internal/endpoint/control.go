package endpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"time"
)

type ManagedUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Enabled      bool   `json:"enabled"`
	DownloadMbps int    `json:"download_mbps"`
	UploadMbps   int    `json:"upload_mbps"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	QuotaBytes   int64  `json:"quota_bytes,omitempty"`
	Revision     int64  `json:"revision"`
}
type AppliedUser struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Enabled        bool   `json:"enabled"`
	DownloadMbps   int    `json:"download_mbps"`
	UploadMbps     int    `json:"upload_mbps"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	Expired        bool   `json:"expired"`
	QuotaBytes     int64  `json:"quota_bytes"`
	QuotaUsedBytes uint64 `json:"quota_used_bytes"`
	QuotaExhausted bool   `json:"quota_exhausted"`
	Revision       int64  `json:"revision"`
}
type ControlState struct {
	Version      int             `json:"version"`
	Generation   uint64          `json:"generation"`
	Capabilities map[string]bool `json:"capabilities"`
	Users        []AppliedUser   `json:"users"`
	Usage        *UsageMeta      `json:"usage,omitempty"`
}
type UsageMeta struct {
	Month       string `json:"month"`
	StartedAt   string `json:"started_at"`
	CreditBytes uint64 `json:"credit_bytes"`
}
type UsageTotals struct {
	ChargedBytes   uint64 `json:"charged_bytes"`
	UploadBytes    uint64 `json:"upload_bytes"`
	DownloadBytes  uint64 `json:"download_bytes"`
	UncertainBytes uint64 `json:"uncertain_bytes"`
}
type UsageRecord struct {
	UserID         string `json:"user_id"`
	Month          string `json:"month"`
	ReservedBytes  uint64 `json:"reserved_bytes"`
	UploadBytes    uint64 `json:"upload_bytes"`
	DownloadBytes  uint64 `json:"download_bytes"`
	ChargedBytes   uint64 `json:"charged_bytes"`
	InFlightBytes  uint64 `json:"in_flight_bytes"`
	UncertainBytes uint64 `json:"uncertain_bytes"`
}
type UsageReport struct {
	UsageMeta
	Months     []string      `json:"months"`
	Records    []UsageRecord `json:"records"`
	NextAfter  *string       `json:"next_after"`
	TotalUsers int           `json:"total_users"`
	Totals     UsageTotals   `json:"totals"`
}
type ControlError struct{ Code string }

func (e *ControlError) Error() string { return "endpoint control: " + e.Code }

type Control struct{ path string }

func NewControl(path string) (*Control, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("control socket path must be absolute")
	}
	return &Control{path: path}, nil
}

type controlReply struct {
	OK                  bool         `json:"ok"`
	Error               string       `json:"error"`
	State               ControlState `json:"state"`
	DisconnectRequested int          `json:"disconnect_requested"`
	Usage               *UsageReport `json:"usage"`
}

func (c *Control) call(ctx context.Context, request any) (controlReply, error) {
	if c == nil {
		return controlReply{}, &ControlError{Code: "not_configured"}
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", c.path)
	if err != nil {
		return controlReply{}, &ControlError{Code: "unavailable"}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	data, err := json.Marshal(request)
	if err != nil {
		return controlReply{}, err
	}
	if len(data) > 65535 {
		return controlReply{}, &ControlError{Code: "request_too_large"}
	}
	data = append(data, '\n')
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return controlReply{}, &ControlError{Code: "unavailable"}
		}
		data = data[n:]
	}
	reader := bufio.NewReaderSize(conn, 65537)
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 65536 {
		return controlReply{}, &ControlError{Code: "invalid_response"}
	}
	var reply controlReply
	if json.Unmarshal(line, &reply) != nil {
		return reply, &ControlError{Code: "invalid_response"}
	}
	if !reply.OK {
		return reply, &ControlError{Code: reply.Error}
	}
	return reply, nil
}
func (c *Control) Status(ctx context.Context) (ControlState, error) {
	r, err := c.call(ctx, map[string]string{"op": "status"})
	if err == nil && r.State.Version != 1 {
		err = &ControlError{Code: "unsupported_version"}
	}
	return r.State, err
}
func (c *Control) Usage(ctx context.Context, month, after string) (*UsageReport, error) {
	r, err := c.call(ctx, map[string]string{"op": "usage", "month": month, "after": after})
	if err == nil && r.Usage == nil {
		return nil, &ControlError{Code: "usage_unavailable"}
	}
	return r.Usage, err
}
func (c *Control) Upsert(ctx context.Context, generation uint64, u ManagedUser) (ControlState, error) {
	r, err := c.call(ctx, struct {
		Op                 string      `json:"op"`
		ExpectedGeneration uint64      `json:"expected_generation"`
		User               ManagedUser `json:"user"`
	}{"upsert", generation, u})
	if err == nil && r.State.Version != 1 {
		err = &ControlError{Code: "unsupported_version"}
	}
	return r.State, err
}
func (c *Control) Disconnect(ctx context.Context, username string) (int, error) {
	r, err := c.call(ctx, map[string]string{"op": "disconnect", "username": username})
	return r.DisconnectRequested, err
}

func (c *Control) Remove(ctx context.Context, generation uint64, id, username string, revision int64) (ControlState, error) {
	r, err := c.call(ctx, map[string]any{"op": "remove", "expected_generation": generation, "id": id, "username": username, "expected_revision": revision})
	if err == nil && r.State.Version != 1 {
		err = &ControlError{Code: "unsupported_version"}
	}
	return r.State, err
}
