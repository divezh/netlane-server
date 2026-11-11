package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/divezh/netlane-server/internal/api"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

// ControlAPI is the managed endpoint's control socket; *endpoint.Control implements it.
type ControlAPI interface {
	Status(ctx context.Context) (endpoint.ControlState, error)
	Upsert(ctx context.Context, generation uint64, u endpoint.ManagedUser) (endpoint.ControlState, error)
	Remove(ctx context.Context, generation uint64, id, username string, revision int64) (endpoint.ControlState, error)
	Disconnect(ctx context.Context, username string) (int, error)
	Usage(ctx context.Context, month, after string) (*endpoint.UsageReport, error)
}

// Agent keeps the local endpoint's users equal to main's snapshot.
type Agent struct {
	MainURL   string // https://<main>/<prefix>
	Token     string
	StatePath string
	Control   ControlAPI
	Clients   func(ctx context.Context) ([]endpoint.Client, error)
	HTTP      *http.Client
	Now       func() time.Time
	Log       func(string)
	// Version is the engine version to report; empty keeps the one sent at join.
	Version    string
	Resources  func() store.Resources
	statistics *store.StatisticsObservation

	revision int64
	acked    []int64
	polling  bool
}

type syncReply struct {
	Revision  int64                   `json:"revision"`
	Unchanged bool                    `json:"unchanged"`
	Users     []store.DistributedUser `json:"users"`
	Commands  []store.Command         `json:"commands"`
}

var errRevoked = errors.New("node token revoked")

// Pauses; variables so tests could shorten them.
var (
	firstBackoff = 2 * time.Second
	revokedRetry = 10 * time.Minute
	pollInterval = 10 * time.Second
	maxBackoff   = 60 * time.Second
)

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agent) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if a.Log != nil {
		a.Log(msg)
	} else {
		log.Print(msg)
	}
}

// LastSync is the time of the last successful sync stored in the state file, or zero.
func LastSync(path string) (time.Time, int64) {
	var st struct {
		Revision int64  `json:"revision"`
		SyncedAt string `json:"synced_at"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &st) != nil {
		return time.Time{}, 0
	}
	t, _ := time.Parse(time.RFC3339, st.SyncedAt)
	return t, st.Revision
}

func (a *Agent) saveState() {
	if a.StatePath == "" {
		return
	}
	data, _ := json.Marshal(map[string]any{"revision": a.revision, "synced_at": a.now().UTC().Format(time.RFC3339)})
	tmp := a.StatePath + ".tmp"
	if os.WriteFile(tmp, data, 0600) == nil {
		os.Rename(tmp, a.StatePath)
	}
}

// Run syncs until ctx ends: long polls main, applies changes, backs off on errors.
func (a *Agent) Run(ctx context.Context) error {
	_, a.revision = LastSync(a.StatePath)
	backoff := firstBackoff
	short := 0
	// Long polls that end early (a proxy cutting idle requests) switch the agent to polling.
	noteShort := func(started time.Time) {
		if a.now().Sub(started) < 20*time.Second {
			short++
		} else {
			short = 0
		}
		if !a.polling && short >= 3 {
			a.polling = true
			a.logf("node: долгие запросы обрываются, перехожу на опрос раз в %v", pollInterval)
		}
	}
	for ctx.Err() == nil {
		started := a.now()
		reply, err := a.sync(ctx)
		wait := time.Duration(0)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, errRevoked):
			a.logf("node: нода удалена на главном сервере; пользователи убраны")
			if err = a.ClearAll(ctx); err != nil {
				a.logf("node: не удалось убрать пользователей: %v", err)
			}
			a.revision = 0
			a.saveState()
			wait = revokedRetry
		case err != nil:
			if cutShort(err) {
				noteShort(started)
			}
			a.logf("node: синхронизация не удалась: %v", err)
			wait, backoff = backoff, min(backoff*2, maxBackoff)
		default:
			backoff = firstBackoff
			if reply.Unchanged {
				if !a.polling {
					noteShort(started)
				}
			} else {
				acked, err := a.Apply(ctx, reply.Users, reply.Commands)
				a.acked = append(a.acked, acked...)
				if err != nil {
					a.logf("node: не удалось применить пользователей: %v", err)
					wait, backoff = backoff, min(backoff*2, maxBackoff)
				} else {
					a.revision = reply.Revision
				}
			}
			a.saveState()
			if a.polling && wait == 0 {
				wait = pollInterval
			}
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
	}
	return nil
}

func (a *Agent) report(ctx context.Context) store.NodeReport {
	r := store.NodeReport{Revision: a.revision, Acked: a.acked, EndpointVersion: a.Version}
	var counters map[string]store.Counter
	seen := map[string]bool{}
	connections := 0
	state, err := a.Control.Status(ctx)
	if err != nil {
		return r
	}
	ids := map[string]string{}
	for _, u := range state.Users {
		r.Users = append(r.Users, store.UserRevision{ID: u.ID, Revision: u.Revision})
		ids[u.Username] = u.ID
	}
	month := a.now().UTC().Format("2006-01")
	after := ""
	for page := 0; page < 50; page++ {
		usage, err := a.Control.Usage(ctx, month, after)
		if err != nil {
			break
		}
		for _, rec := range usage.Records {
			r.Usage = append(r.Usage, store.UsageRow{UserID: rec.UserID, Month: usage.Month, UploadBytes: rec.UploadBytes, DownloadBytes: rec.DownloadBytes})
		}
		if usage.NextAfter == nil {
			break
		}
		after = *usage.NextAfter
	}
	if a.Clients != nil {
		if clients, err := a.Clients(ctx); err == nil {
			counters = map[string]store.Counter{}
			stamp := a.now().UTC().Format(time.RFC3339)
			for _, c := range clients {
				counter := counters[c.Username]
				counter.Download += c.Outbound
				counter.Upload += c.Inbound
				counters[c.Username] = counter
				if id := ids[c.Username]; id != "" && c.Sessions > 0 {
					seen[c.Username] = true
					connections += int(c.Sessions)
					p := store.PresenceRow{UserID: id, LastSeenAt: stamp}
					if c.IP != nil {
						p.LastIP = *c.IP
					}
					r.Presence = append(r.Presence, p)
				}
			}
		}
	}
	if a.Resources != nil {
		if a.statistics == nil || a.now().Unix()-a.statistics.Sample.At >= 60 {
			sample := store.StatisticsSample{At: a.now().Unix(), Resources: a.Resources()}
			if counters != nil {
				count := len(seen)
				sample.Online = &count
				sample.Connections = &connections
			}
			a.statistics = &store.StatisticsObservation{Sample: sample, Counters: counters}
		}
		r.Statistics = a.statistics
	}
	return r
}

func (a *Agent) sync(ctx context.Context) (syncReply, error) {
	body, _ := json.Marshal(a.report(ctx))
	payload, err := api.EncodePayload(body)
	if err != nil {
		return syncReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.MainURL+"/node/sync", nil)
	if err != nil {
		return syncReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set(api.PayloadHeader, payload)
	if a.polling {
		req.Header.Set(api.PollHeader, "1")
	}
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: 40 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return syncReply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return syncReply{}, errRevoked
	}
	if resp.StatusCode != 200 {
		return syncReply{}, fmt.Errorf("главный сервер ответил %d", resp.StatusCode)
	}
	var reply syncReply
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&reply); err != nil {
		return syncReply{}, err
	}
	a.acked = nil // main stored them with this report
	return reply, nil
}

func conflict(err error) bool {
	var ce *endpoint.ControlError
	return errors.As(err, &ce) && (ce.Code == "generation_conflict" || ce.Code == "revision_conflict")
}

func same(live endpoint.AppliedUser, u store.DistributedUser, expiry bool) bool {
	expires := u.ExpiresAt
	if !expiry {
		expires = ""
	}
	return live.Username == u.Username && live.Enabled == u.Enabled && live.DownloadMbps == u.DownloadMbps && live.UploadMbps == u.UploadMbps && live.ExpiresAt == expires && live.Revision == u.Revision
}

// Apply makes the endpoint hold exactly the given users and runs the commands; it returns
// the ids of the commands it ran. Users the endpoint refuses do not stop the others: the
// error lists them and the next sync tries again.
func (a *Agent) Apply(ctx context.Context, users []store.DistributedUser, cmds []store.Command) ([]int64, error) {
	state, err := a.Control.Status(ctx)
	if err != nil {
		return nil, err
	}
	want, names := map[string]bool{}, map[string]string{}
	for _, u := range users {
		want[u.ID], names[u.Username] = true, u.ID
	}
	var problems []string
	failed := func(name string, err error) {
		problems = append(problems, name+": "+err.Error())
		if fresh, e := a.Control.Status(ctx); e == nil {
			state = fresh
		}
	}
	// Removals first: a name re-created on main under a new id must be free before its upsert.
	for _, live := range append([]endpoint.AppliedUser(nil), state.Users...) {
		holder := names[live.Username] != "" && names[live.Username] != live.ID
		if want[live.ID] && !holder {
			continue
		}
		next, err := a.drop(ctx, state, live)
		if err != nil {
			failed(live.Username, err)
			continue
		}
		state = next
	}
	for _, u := range users {
		var live *endpoint.AppliedUser
		for i := range state.Users {
			if state.Users[i].ID == u.ID {
				live = &state.Users[i]
			}
		}
		if live != nil && same(*live, u, state.Capabilities["expiry"]) {
			continue
		}
		next, err := a.upsert(ctx, state, u)
		// main restored from a backup: its revision is older than the endpoint's; replace the user.
		if err != nil && live != nil && live.Revision > u.Revision && conflict(err) {
			if state, err = a.drop(ctx, state, *live); err == nil {
				next, err = a.upsert(ctx, state, u)
			}
		}
		if err != nil {
			failed(u.Username, err)
			continue
		}
		state = next
	}
	var acked []int64
	for _, c := range cmds {
		if c.Kind == "disconnect" {
			if _, err = a.Control.Disconnect(ctx, c.Username); err != nil {
				var ce *endpoint.ControlError
				if !errors.As(err, &ce) {
					return acked, err
				}
			}
		}
		acked = append(acked, c.ID)
	}
	if len(problems) > 0 {
		return acked, errors.New(strings.Join(problems, "; "))
	}
	return acked, nil
}

func (a *Agent) upsert(ctx context.Context, state endpoint.ControlState, u store.DistributedUser) (endpoint.ControlState, error) {
	return a.retry(ctx, state, func(s endpoint.ControlState) (endpoint.ControlState, error) {
		m := endpoint.ManagedUser{ID: u.ID, Username: u.Username, Password: u.Password, Enabled: u.Enabled, DownloadMbps: u.DownloadMbps, UploadMbps: u.UploadMbps, ExpiresAt: u.ExpiresAt, Revision: u.Revision}
		if !s.Capabilities["expiry"] {
			m.ExpiresAt = ""
		}
		return a.Control.Upsert(ctx, s.Generation, m)
	})
}

// cutShort: the request reached main but the connection ended without an answer.
func cutShort(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset")
}

// retry runs op and, on a generation conflict, re-reads the state and runs it once more.
func (a *Agent) retry(ctx context.Context, state endpoint.ControlState, op func(endpoint.ControlState) (endpoint.ControlState, error)) (endpoint.ControlState, error) {
	next, err := op(state)
	if err == nil || !conflict(err) {
		return next, err
	}
	if state, err = a.Control.Status(ctx); err != nil {
		return state, err
	}
	return op(state)
}

// drop removes a user, or disables it on an endpoint that cannot remove users.
func (a *Agent) drop(ctx context.Context, state endpoint.ControlState, live endpoint.AppliedUser) (endpoint.ControlState, error) {
	if state.Capabilities["remove_user"] {
		return a.retry(ctx, state, func(s endpoint.ControlState) (endpoint.ControlState, error) {
			return a.Control.Remove(ctx, s.Generation, live.ID, live.Username, live.Revision)
		})
	}
	if !live.Enabled {
		return state, nil
	}
	return state, errors.New("endpoint cannot remove users")
}

// ClearAll removes every user from the endpoint (the node was deleted on main).
func (a *Agent) ClearAll(ctx context.Context) error {
	_, err := a.Apply(ctx, nil, nil)
	return err
}
