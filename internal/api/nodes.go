package api

import (
	"context"
	"database/sql"
	"errors"
	"github.com/divezh/netlane-server/internal/qrcode"
	"net/http"
	"strings"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
	"github.com/divezh/netlane-server/internal/update"
)

type nodeView struct {
	store.Node
	Online bool `json:"online"`
}

func online(n store.Node, now time.Time) bool {
	seen, err := time.Parse(time.RFC3339, n.LastSeenAt)
	return err == nil && now.Sub(seen) <= NodeOnline
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.db.ListNodes(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	revision, err := s.db.UsersRevision(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	views := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		views = append(views, nodeView{Node: n, Online: online(n, time.Now())})
	}
	respond(w, 200, map[string]any{"nodes": views, "users_revision": revision, "sync_prefix_set": s.cfg.SyncPrefix != ""})
}

func (s *Server) nodeDetails(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	month := now.Format("2006-01")
	node, revision, users, err := s.db.NodeDetails(r.Context(), r.PathValue("id"), month)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "node_not_found")
		return
	}
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	respond(w, 200, map[string]any{
		"node":           nodeView{Node: node, Online: online(node, now)},
		"users_revision": revision, "users": users, "month": month,
	})
}

// inviteNode issues the command that turns a fresh VPS into a node of this server.
func (s *Server) inviteNode(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SyncPrefix == "" {
		problem(w, 409, "sync_prefix_missing")
		return
	}
	users, err := s.db.List(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	var quota []string
	for _, u := range users {
		if u.Policy.QuotaBytes > 0 {
			quota = append(quota, u.Username)
		}
	}
	if len(quota) > 0 {
		respond(w, 409, map[string]any{"error": "quota_set_remove_first", "users": quota})
		return
	}
	code, expires, err := s.db.CreateInvite(r.Context(), s.cfg.Admin, time.Now())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	join := "https://" + s.cfg.Domain + "/" + s.cfg.SyncPrefix + "/node/join#" + code
	command := `cd /root && ` + update.VerifyCommand() + ` && netlane_node_dir=$(mktemp -d /root/netlane-node.XXXXXX) && tar -xzf trusttunnel-dev.tar.gz -C "$netlane_node_dir" && bash "$netlane_node_dir/setup.sh" --join '` + join + "'"
	respond(w, 200, map[string]any{"command": command, "expires_at": expires.Format(time.RFC3339)})
}

// updateNode changes the name and country clients show for a node.
func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    string `json:"name"`
		Country string `json:"country"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Country = strings.TrimSpace(input.Name), strings.ToUpper(strings.TrimSpace(input.Country))
	if config.ValidateServerIdentity(input.Name, input.Country) != nil {
		problem(w, 400, "invalid_node_identity")
		return
	}
	node, err := s.db.UpdateNode(r.Context(), s.cfg.Admin, r.PathValue("id"), input.Name, input.Country)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "node_not_found")
		return
	}
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	respond(w, 200, map[string]any{"node": nodeView{Node: node, Online: online(node, time.Now())}})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	err := s.db.DeleteNode(r.Context(), s.cfg.Admin, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "node_not_found")
		return
	}
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	s.hub.Notify()
	w.WriteHeader(204)
}

// quotaAllowed refuses a monthly quota while nodes exist (they count traffic separately).
func (s *Server) quotaAllowed(w http.ResponseWriter, r *http.Request, p store.Policy) bool {
	if p.QuotaBytes <= 0 {
		return true
	}
	hasNodes, err := s.db.HasNodes(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return false
	}
	if hasNodes {
		problem(w, 409, "quota_not_supported_with_nodes")
		return false
	}
	return true
}

type locations struct {
	Applied int `json:"applied"`
	Total   int `json:"total"`
}

type userView struct {
	store.User
	Locations *locations `json:"locations,omitempty"`
	Group     *groupRef  `json:"group,omitempty"`
}

// usersWithLocations adds, when nodes exist, on how many locations each user is applied.
// Locations are the ones the user's group covers: a user of a restricted group is never
// applied to the nodes outside it, so those must not count as missing.
func (s *Server) usersWithLocations(ctx context.Context) ([]userView, error) {
	users, err := s.db.List(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.db.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	membership, err := s.db.UserGroups(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.db.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	// allowed is the user's group badge and the nodes that group covers. A user
	// without a group row counts as the default group and covers every node.
	allowed := func(userID string) (*groupRef, []store.Node) {
		group, ok := byID[membership[userID]]
		if !ok {
			return nil, nodes
		}
		ref := groupRef{ID: group.ID, Name: group.Name}
		allowedNodes := make([]store.Node, 0, len(nodes))
		for _, n := range nodes {
			if group.Covers(n.ID) {
				allowedNodes = append(allowedNodes, n)
			}
		}
		return &ref, allowedNodes
	}
	views := make([]userView, 0, len(users))
	if len(nodes) == 0 {
		for _, u := range users {
			ref, _ := allowed(u.ID)
			views = append(views, userView{User: u, Group: ref})
		}
		return views, nil
	}
	states, err := s.db.NodeUserStates(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, n := range nodes {
		live[n.ID] = online(n, time.Now())
	}
	for _, u := range users {
		ref, allowedNodes := allowed(u.ID)
		permitted := make(map[string]bool, len(allowedNodes))
		for _, n := range allowedNodes {
			permitted[n.ID] = true
		}
		l := &locations{Total: 1 + len(allowedNodes)}
		if u.AppliedRevision > 0 && u.AppliedRevision == u.DesiredRevision {
			l.Applied++
		}
		for _, st := range states[u.ID] {
			if permitted[st.NodeID] && live[st.NodeID] && st.Revision == u.DesiredRevision {
				l.Applied++
			}
		}
		views = append(views, userView{User: u, Locations: l, Group: ref})
	}
	return views, nil
}

func (s *Server) subscriptionLink(w http.ResponseWriter, r *http.Request, u store.User) {
	var input struct {
		Rotate bool `json:"rotate"`
	}
	if !decode(w, r, &input) {
		return
	}
	if s.cfg.SyncPrefix == "" {
		problem(w, 409, "sync_prefix_missing")
		return
	}
	token, err := s.db.Subscription(r.Context(), s.cfg.Admin, u.ID, input.Rotate)
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	url := "https://" + s.cfg.Domain + "/" + s.cfg.SyncPrefix + "/sub/" + token
	picture, _ := qrcode.DataURI(url)
	respond(w, 200, map[string]string{"url": url, "qr": picture})
}

// addNodeUsage adds what nodes reported for the month to the endpoint's usage report.
func (s *Server) addNodeUsage(ctx context.Context, usage *endpoint.UsageReport) error {
	extra, err := s.db.NodeUsage(ctx, usage.Month)
	if err != nil || len(extra) == 0 {
		return err
	}
	for _, row := range extra {
		usage.Totals.UploadBytes += row.UploadBytes
		usage.Totals.DownloadBytes += row.DownloadBytes
		usage.Totals.ChargedBytes += row.UploadBytes + row.DownloadBytes
	}
	for i := range usage.Records {
		rec := &usage.Records[i]
		if row, ok := extra[rec.UserID]; ok {
			rec.UploadBytes += row.UploadBytes
			rec.DownloadBytes += row.DownloadBytes
			rec.ChargedBytes += row.UploadBytes + row.DownloadBytes
			delete(extra, rec.UserID)
		}
	}
	for _, row := range extra {
		if usage.NextAfter == nil {
			usage.Records = append(usage.Records, endpoint.UsageRecord{UserID: row.UserID, Month: row.Month, UploadBytes: row.UploadBytes, DownloadBytes: row.DownloadBytes, ChargedBytes: row.UploadBytes + row.DownloadBytes})
		}
	}
	return nil
}
