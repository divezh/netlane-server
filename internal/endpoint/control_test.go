//go:build !windows

package endpoint

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func controlSocket(t *testing.T, reply func(map[string]any) any) *Control {
	t.Helper()
	dir, err := os.MkdirTemp("", "tt-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request map[string]any
				if json.NewDecoder(conn).Decode(&request) == nil {
					_ = json.NewEncoder(conn).Encode(reply(request))
				}
			}()
		}
	}()
	client, err := NewControl(path)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func TestControlProtocolAndConflict(t *testing.T) {
	client := controlSocket(t, func(request map[string]any) any {
		switch request["op"] {
		case "status":
			return map[string]any{"ok": true, "state": ControlState{Version: 1, Generation: 7}}
		case "upsert":
			if request["expected_generation"] != float64(7) {
				return map[string]any{"ok": false, "error": "generation_conflict"}
			}
			u := request["user"].(map[string]any)
			if u["password"] != "test-credential-123" {
				return map[string]any{"ok": false, "error": "invalid_user"}
			}
			return map[string]any{"ok": true, "state": ControlState{Version: 1, Generation: 8, Users: []AppliedUser{{ID: "u1", Username: "alice", Revision: 2}}}}
		case "disconnect":
			return map[string]any{"ok": true, "disconnect_requested": 2}
		case "usage":
			if request["month"] != "2026-11" || request["after"] != "u1" {
				t.Error("usage query lost pagination or month")
			}
			return map[string]any{"ok": true, "usage": UsageReport{UsageMeta: UsageMeta{Month: "2026-11", CreditBytes: 1048576}, Records: []UsageRecord{{UserID: "u2", ChargedBytes: 128}}, TotalUsers: 2, Totals: UsageTotals{ChargedBytes: 256}}}
		}
		return map[string]any{"ok": false, "error": "invalid_request"}
	})
	state, err := client.Status(context.Background())
	if err != nil || state.Generation != 7 {
		t.Fatalf("status: %v", err)
	}
	user := ManagedUser{ID: "u1", Username: "alice", Password: "test-credential-123", Enabled: true, Revision: 2}
	if _, err = client.Upsert(context.Background(), 6, user); err == nil {
		t.Fatal("stale generation accepted")
	}
	state, err = client.Upsert(context.Background(), 7, user)
	if err != nil || state.Generation != 8 || len(state.Users) != 1 {
		t.Fatalf("upsert: %v", err)
	}
	n, err := client.Disconnect(context.Background(), "alice")
	if err != nil || n != 2 {
		t.Fatal("disconnect reply mismatch")
	}
	usage, err := client.Usage(context.Background(), "2026-11", "u1")
	if err != nil || usage.Month != "2026-11" || usage.Totals.ChargedBytes != 256 || len(usage.Records) != 1 {
		t.Fatal("usage report or pagination not preserved")
	}
}
func TestControlReadHonorsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	client := controlSocket(t, func(map[string]any) any { <-release; return map[string]any{"ok": true} })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := client.Status(ctx); err == nil {
		t.Fatal("cancelled request succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("read ignored cancellation")
	}
}
func TestUnconfiguredControlIsNotAdvertisedAsReady(t *testing.T) {
	client, err := NewControl("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Status(context.Background()); err == nil {
		t.Fatal("nil control reported ready")
	}
}
