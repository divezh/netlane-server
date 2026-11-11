package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Resources struct {
	CPU         *float64 `json:"cpu"`
	MemoryUsed  *uint64  `json:"memory_used"`
	MemoryTotal *uint64  `json:"memory_total"`
	DiskUsed    *uint64  `json:"disk_used"`
	DiskTotal   *uint64  `json:"disk_total"`
	Cores       int      `json:"cores"`
}
type Counter struct {
	Download uint64 `json:"download"`
	Upload   uint64 `json:"upload"`
}
type StatisticsSample struct {
	At          int64   `json:"at"`
	Online      *int    `json:"online"`
	Connections *int    `json:"connections,omitempty"`
	Download    *uint64 `json:"download"`
	Upload      *uint64 `json:"upload"`
	Seconds     int64   `json:"seconds"`
	Resources
}

type StatisticsObservation struct {
	Sample   StatisticsSample   `json:"sample"`
	Counters map[string]Counter `json:"counters"`
}

// RecordStatistics preserves the last baseline across panel restarts. Missing metrics,
// resets, membership changes and gaps over 90s break continuity rather than invent traffic.
func (s *Store) RecordStatistics(ctx context.Context, sample StatisticsSample, counters map[string]Counter) error {
	return s.RecordLocationStatistics(ctx, "main", sample, counters)
}

func (s *Store) RecordLocationStatistics(ctx context.Context, source string, sample StatisticsSample, counters map[string]Counter) error {
	sample.Download, sample.Upload, sample.Seconds = nil, nil, 0
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if source != "main" {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE id=?", source).Scan(&exists); err != nil {
			return err
		}
	}
	var at int64
	var encoded string
	err = tx.QueryRowContext(ctx, "SELECT at,counters FROM location_statistics_baseline WHERE source=?", source).Scan(&at, &encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && sample.At <= at {
		return nil
	}
	var previous map[string]Counter
	if encoded != "" {
		if err = json.Unmarshal([]byte(encoded), &previous); err != nil {
			return err
		}
	}
	seconds := sample.At - at
	if previous != nil && counters != nil && seconds > 0 && seconds <= 90 {
		var down, up uint64
		valid := len(counters) == len(previous)
		for name, now := range counters {
			old, ok := previous[name]
			if !ok || now.Download < old.Download || now.Upload < old.Upload {
				valid = false
				break
			}
			down += now.Download - old.Download
			up += now.Upload - old.Upload
		}
		if valid {
			sample.Download = &down
			sample.Upload = &up
			sample.Seconds = seconds
		}
	}
	body, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO location_statistics_samples(source,at,data) VALUES(?,?,?)", source, sample.At, string(body)); err != nil {
		return err
	}
	baseline, err := json.Marshal(counters)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO location_statistics_baseline(source,at,counters) VALUES(?,?,?) ON CONFLICT(source) DO UPDATE SET at=excluded.at,counters=excluded.counters", source, sample.At, string(baseline)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM location_statistics_samples WHERE source=? AND at<?", source, sample.At-31*86400); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM subscription_visits WHERE day<?", time.Unix(sample.At, 0).UTC().AddDate(0, 0, -31).Format("2006-01-02")); err != nil {
		return err
	}
	return tx.Commit()
}

type ClientCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type StatisticsReport struct {
	Location   string             `json:"location,omitempty"`
	NodeOnline *bool              `json:"node_online,omitempty"`
	ObservedAt int64              `json:"observed_at"`
	StartedAt  *int64             `json:"started_at"`
	Latest     *StatisticsSample  `json:"latest"`
	Points     []StatisticsSample `json:"points"`
	Download24 uint64             `json:"download_24h"`
	Upload24   uint64             `json:"upload_24h"`
	Coverage24 int64              `json:"coverage_24h_seconds"`
	Clients    []ClientCount      `json:"clients"`
	ClientDay  string             `json:"client_day"`
	Scope      string             `json:"scope"`
	Interval   int                `json:"sample_interval_seconds"`
}

// Raw minute samples are coalesced at the API boundary; at most 721 points reach the browser.
func (s *Store) Statistics(ctx context.Context, now time.Time, period time.Duration, day string) (StatisticsReport, error) {
	return s.LocationStatistics(ctx, "main", now, period, day)
}

func (s *Store) LocationStatistics(ctx context.Context, source string, now time.Time, period time.Duration, day string) (StatisticsReport, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return StatisticsReport{}, err
	}
	defer tx.Rollback()
	out := StatisticsReport{ObservedAt: now.Unix(), Points: []StatisticsSample{}, Clients: []ClientCount{}, ClientDay: day, Scope: source, Interval: 60}
	var first sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MIN(at) FROM location_statistics_samples WHERE source=?", source).Scan(&first); err != nil {
		return out, err
	}
	if first.Valid {
		out.StartedAt = &first.Int64
	}
	var latest string
	err = tx.QueryRowContext(ctx, "SELECT data FROM location_statistics_samples WHERE source=? ORDER BY at DESC LIMIT 1", source).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.Latest = &StatisticsSample{}
		if err = json.Unmarshal([]byte(latest), out.Latest); err != nil {
			return out, err
		}
	}
	from := now.Add(-period).Unix()
	since := from
	if lastDay := now.Add(-24 * time.Hour).Unix(); lastDay < since {
		since = lastDay
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM location_statistics_samples WHERE source=? AND at>=? AND at<=? ORDER BY at", source, since, now.Unix())
	if err != nil {
		return out, err
	}
	step := int64(period.Seconds() / 360)
	if step < 60 {
		step = 60
	}
	// Aggregate resources as the mean of known samples, retain missing values as null.
	type bucket struct {
		s         StatisticsSample
		n         [5]uint64
		totals    [5]float64
		onlineN   int
		onlineSum int
	}
	buckets := map[int64]*bucket{}
	order := []int64{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return out, err
		}
		var p StatisticsSample
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			rows.Close()
			return out, err
		}
		// Only intervals wholly inside the rolling window are counted. Never extrapolate unknown history.
		if p.At-p.Seconds >= now.Add(-24*time.Hour).Unix() && p.Download != nil {
			out.Download24 += *p.Download
			out.Upload24 += *p.Upload
			out.Coverage24 += p.Seconds
		}
		if p.At < from {
			continue
		}
		key := p.At / step
		b := buckets[key]
		if b == nil {
			b = &bucket{s: StatisticsSample{At: p.At}}
			buckets[key] = b
			order = append(order, key)
		}
		b.s.At = p.At
		b.s.Cores = p.Cores
		if p.Online != nil {
			b.onlineN++
			b.onlineSum += *p.Online
		}
		if p.At-p.Seconds >= from && p.Download != nil {
			if b.s.Download == nil {
				var a, c uint64
				b.s.Download = &a
				b.s.Upload = &c
			}
			*b.s.Download += *p.Download
			*b.s.Upload += *p.Upload
			b.s.Seconds += p.Seconds
		}
		vals := []*uint64{p.MemoryUsed, p.MemoryTotal, p.DiskUsed, p.DiskTotal}
		if p.CPU != nil {
			b.totals[0] += *p.CPU
			b.n[0]++
		}
		for i, v := range vals {
			if v != nil {
				b.totals[i+1] += float64(*v)
				b.n[i+1]++
			}
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for _, key := range order {
		b := buckets[key]
		if b.onlineN > 0 {
			v := int(float64(b.onlineSum)/float64(b.onlineN) + .5)
			b.s.Online = &v
		}
		if b.n[0] > 0 {
			v := b.totals[0] / float64(b.n[0])
			b.s.CPU = &v
		}
		targets := []**uint64{&b.s.MemoryUsed, &b.s.MemoryTotal, &b.s.DiskUsed, &b.s.DiskTotal}
		for i, t := range targets {
			if b.n[i+1] > 0 {
				v := uint64(b.totals[i+1] / float64(b.n[i+1]))
				*t = &v
			}
		}
		out.Points = append(out.Points, b.s)
	}
	if source != "main" {
		return out, nil
	}
	clients, err := tx.QueryContext(ctx, "SELECT client,COUNT(*) FROM subscription_visits WHERE day=? GROUP BY client ORDER BY COUNT(*) DESC,client", day)
	if err != nil {
		return out, err
	}
	defer clients.Close()
	for clients.Next() {
		var c ClientCount
		if err = clients.Scan(&c.Name, &c.Count); err != nil {
			return out, err
		}
		out.Clients = append(out.Clients, c)
	}
	return out, clients.Err()
}

var netlaneVersion = regexp.MustCompile(`(?:^|[\s(;])netlane/(\d{1,3}\.\d{1,3}\.\d{1,4})(?:$|[\s);])`)

// Only a hash of a high-entropy subscription token and a bounded application category
// are retained. The last successful request owns that token's single daily category.
func (s *Store) RecordSubscriptionVisit(ctx context.Context, at time.Time, token, userAgent string) error {
	app := "Не определено"
	ua := strings.ToLower(userAgent)
	if len(ua) > 512 {
		ua = ua[:512]
	}
	for _, c := range []struct{ match, name string }{{"netlane", "Netlane"}, {"wintrusttunnel", "Netlane"}, {"trusttunnel", "TrustTunnel"}, {"clash", "Clash"}, {"happ", "Happ"}, {"hiddify", "Hiddify"}, {"sing-box", "sing-box"}, {"v2ray", "v2ray"}} {
		if strings.Contains(ua, c.match) {
			app = c.name
			break
		}
	}
	// The Windows client sends "Netlane/X.Y.Z": the version stays bounded (three short numbers),
	// so the category set is still small and the panel can show who has not updated.
	if m := netlaneVersion.FindStringSubmatch(ua); m != nil {
		app = "Netlane " + m[1]
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO subscription_visits(day,token_hash,client) VALUES(?,?,?) ON CONFLICT(day,token_hash) DO UPDATE SET client=excluded.client", at.UTC().Format("2006-01-02"), secretHash(token), app)
	return err
}
