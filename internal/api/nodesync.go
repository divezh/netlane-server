package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

// longPoll is how long main holds a node's sync request waiting for a change.
var longPoll = 25 * time.Second

// NodeOnline is how recent a node's last sync must be for it to count as connected.
const NodeOnline = 2 * time.Minute

// SubscriptionWindow: only nodes on line now are in subscriptions, so a node that is off or
// whose VPS is gone leaves clients at their next refresh (a node syncs at least every 25 s).
const SubscriptionWindow = NodeOnline

// SubscriptionHours is how often clients refresh the subscription.
const SubscriptionHours = 12

var validDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type syncHandler struct {
	cfg  config.Config
	db   *store.Store
	hub  *Hub
	site http.Handler
	now  func() time.Time
	// quotas reads the applied monthly quota and its use from the managed endpoint; nil without one.
	quotas quotaSource
}

// quotaSource is the part of the endpoint control the subscription needs.
type quotaSource interface {
	Status(ctx context.Context) (endpoint.ControlState, error)
}

// SyncHandler serves the node protocol and subscriptions under /<sync_prefix>/ and passes
// everything else, and every request it does not accept, to the decoy site unchanged.
func SyncHandler(cfg config.Config, db *store.Store, hub *Hub, site http.Handler, now func() time.Time) http.Handler {
	h := &syncHandler{cfg: cfg, db: db, hub: hub, site: site, now: now}
	// Quotas live only on the main server's endpoint (a quota and nodes do not mix).
	if control, err := endpoint.NewControl(cfg.ControlSocket); err == nil && control != nil {
		h.quotas = control
	}
	return h
}

func (h *syncHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix := "/" + h.cfg.SyncPrefix + "/"
	if h.cfg.SyncPrefix == "" || !strings.HasPrefix(r.URL.Path, prefix) {
		h.site.ServeHTTP(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case r.Method == "POST" && rest == "node/join":
		h.join(w, r)
	case r.Method == "POST" && rest == "node/sync":
		h.sync(w, r)
	case r.Method == "GET" && strings.HasPrefix(rest, "sub/") && !strings.Contains(rest[4:], "/") && rest != "sub/":
		h.subscription(w, r, rest[4:])
	default:
		h.site.ServeHTTP(w, r)
	}
}

func (h *syncHandler) join(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
		store.NodeJoin
	}
	data, err := readPayload(r, 4096)
	if err != nil || json.Unmarshal(data, &input) != nil || input.Code == "" || !validDomain.MatchString(input.Domain) || len(input.Domain) > 253 || config.ValidateServerIdentity(input.Name, input.Country) != nil || len(input.EndpointVersion) > 64 {
		h.site.ServeHTTP(w, r)
		return
	}
	node, token, err := h.db.JoinNode(r.Context(), input.Code, input.NodeJoin, h.now())
	if err != nil {
		if !errors.Is(err, store.ErrInvalidInvite) {
			log.Printf("nodes: join failed: %v", err)
		}
		h.site.ServeHTTP(w, r)
		return
	}
	log.Printf("nodes: %s (%s) joined", node.ID, node.Domain)
	h.hub.Notify()
	respond(w, 200, map[string]string{"node_id": node.ID, "node_token": token})
}

func (h *syncHandler) sync(w http.ResponseWriter, r *http.Request) {
	// Without a bearer token this is any visitor: the decoy answers. Only a wrong token gets 401.
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == r.Header.Get("Authorization") {
		h.site.ServeHTTP(w, r)
		return
	}
	node, err := h.db.NodeByToken(r.Context(), token)
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	var report store.NodeReport
	data, err := readPayload(r, 4<<20)
	if err == nil {
		err = json.Unmarshal(data, &report)
	}
	if err != nil {
		problem(w, 400, "invalid_report")
		return
	}
	if report.Statistics != nil && !validNodeStatistics(report.Statistics, h.now()) {
		problem(w, 400, "invalid_statistics")
		return
	}
	ticket := h.hub.Ticket()
	if err = h.db.RecordNodeReport(r.Context(), node.ID, report, h.now()); err != nil {
		problem(w, 500, "storage_error")
		return
	}
	if report.Statistics != nil {
		if err = h.db.RecordLocationStatistics(r.Context(), node.ID, report.Statistics.Sample, report.Statistics.Counters); err != nil {
			problem(w, 500, "storage_error")
			return
		}
	}
	// X-Sync-Poll: the node polls (its long polls get cut), so main answers without waiting.
	wait := longPoll
	if r.Header.Get(PollHeader) != "" {
		wait = 0
	}
	if !h.pending(r.Context(), node.ID, report.Revision) && (wait == 0 || !h.hub.Wait(r.Context(), ticket, wait)) {
		if revision, err := h.db.UsersRevision(r.Context()); err == nil && revision == report.Revision {
			respond(w, 200, map[string]any{"revision": revision, "unchanged": true})
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	revision, err := h.db.UsersRevision(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	// Only the users of the groups attached to this node: a user's group decides
	// which locations the user may use, so the node must not receive everyone.
	users, err := h.db.DistributedForNode(r.Context(), node.ID)
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	commands, err := h.db.PendingCommands(r.Context(), node.ID)
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	respond(w, 200, map[string]any{"revision": revision, "users": users, "commands": commands})
}

// pending reports whether the node is behind or has commands to run.
func (h *syncHandler) pending(ctx context.Context, nodeID string, revision int64) bool {
	current, err := h.db.UsersRevision(ctx)
	if err != nil || current != revision {
		return true
	}
	commands, err := h.db.PendingCommands(ctx, nodeID)
	return err != nil || len(commands) > 0
}

type feedServer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Link    string `json:"link"`
}

func (h *syncHandler) subscription(w http.ResponseWriter, r *http.Request, token string) {
	u, err := h.db.UserBySubscription(r.Context(), token)
	if err != nil {
		h.site.ServeHTTP(w, r)
		return
	}
	name, country := h.hub.Identity()
	mainName := profileName(config.Config{ServerName: name, ServerCountry: country})
	feed := map[string]any{"version": 1, "name": mainName, "update_hours": SubscriptionHours, "servers": []feedServer{}}
	if mainName == "" {
		feed["name"] = h.cfg.Domain
	}
	// What the nodes have (the last apply), not a saved but unapplied edit.
	applied, err := h.db.DistributedUserByID(r.Context(), u.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 500, "storage_error")
		return
	}
	isApplied := err == nil
	if isApplied && usable(applied, h.now()) {
		// The servers a user may pick are the locations of the user's group; the main
		// server is always there because it is the entry point and hosts the panel.
		locations, err := h.db.LocationsForUser(r.Context(), u.ID)
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		password := applied.Password
		link := func(domain, name string) string {
			return profileLink(domain, net.JoinHostPort(domain, "443"), u.Username, password, "http2", false, name)
		}
		servers := []feedServer{{ID: "main", Name: mainName, Country: country, Link: link(h.cfg.Domain, mainName)}}
		nodes, err := h.db.ListNodes(r.Context())
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		for _, n := range nodes {
			seen, err := time.Parse(time.RFC3339, n.LastSeenAt)
			if err != nil || h.now().Sub(seen) > SubscriptionWindow {
				continue
			}
			if !store.CoversLocation(locations, n.ID) {
				continue
			}
			nodeName := profileName(config.Config{ServerName: n.Name, ServerCountry: n.Country})
			servers = append(servers, feedServer{ID: n.ID, Name: nodeName, Country: n.Country, Link: link(n.Domain, nodeName)})
		}
		feed["servers"] = servers
		settings, err := h.db.ClientSettings(r.Context())
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		feed["client_settings"] = settings
	}
	feed["account"] = h.account(r.Context(), u, applied, isApplied)
	if isApplied && usable(applied, h.now()) {
		if r.Method == http.MethodGet {
			if err := h.db.RecordSubscriptionVisit(r.Context(), h.now(), token, r.UserAgent()); err != nil {
				log.Printf("subscription statistics: storage write failed")
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, feed)
}

// account is what the client shows about the access itself: whether it works, until when and
// how much of the monthly quota is used. It holds no credentials.
func (h *syncHandler) account(ctx context.Context, u store.User, applied store.DistributedUser, isApplied bool) map[string]any {
	out := map[string]any{"status": "active"}
	switch {
	case !isApplied:
		out["status"] = "pending"
		return out
	case !applied.Enabled:
		out["status"] = "disabled"
	case !usable(applied, h.now()):
		out["status"] = "expired"
	}
	if applied.ExpiresAt != "" {
		out["expires_at"] = applied.ExpiresAt
	}
	if h.quotas == nil {
		return out
	}
	quick, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	state, err := h.quotas.Status(quick)
	if err != nil {
		// Without the endpoint the client still gets the status and the date.
		return out
	}
	for _, a := range state.Users {
		if a.ID != u.ID || a.QuotaBytes <= 0 {
			continue
		}
		out["quota_bytes"] = a.QuotaBytes
		out["quota_used_bytes"] = a.QuotaUsedBytes
		if state.Usage != nil && state.Usage.Month != "" {
			out["quota_month"] = state.Usage.Month
		}
		if a.QuotaExhausted && out["status"] == "active" {
			out["status"] = "exhausted"
		}
	}
	return out
}

// usable: the applied snapshot is enabled and not expired.
func usable(u store.DistributedUser, now time.Time) bool {
	if !u.Enabled {
		return false
	}
	if u.ExpiresAt != "" {
		deadline, err := time.Parse(time.RFC3339, u.ExpiresAt)
		if err != nil || !now.Before(deadline) {
			return false
		}
	}
	return true
}
