package api

import (
	"context"
	"log"
	"math"
	"net/http"
	"path/filepath"
	"time"

	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/hoststats"
	"github.com/divezh/netlane-server/internal/store"
)

func (s *Server) statistics(w http.ResponseWriter, r *http.Request) {
	periods := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}
	duration, ok := periods[period]
	if !ok {
		problem(w, 400, "invalid_statistics_period")
		return
	}
	now := time.Now().UTC()
	day := r.URL.Query().Get("day")
	if day == "" {
		day = now.Format("2006-01-02")
	}
	date, err := time.Parse("2006-01-02", day)
	if err != nil || date.After(now) || date.Before(now.Truncate(24*time.Hour).AddDate(0, 0, -30)) {
		problem(w, 400, "invalid_statistics_day")
		return
	}
	source := r.URL.Query().Get("node")
	if source == "" {
		source = "main"
	}
	location := ""
	var nodeOnline *bool
	if source != "main" {
		nodes, err := s.db.ListNodes(r.Context())
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		found := false
		for _, n := range nodes {
			if n.ID == source {
				found = true
				location = n.Name
				if location == "" {
					location = n.Domain
				}
				value := online(n, now)
				nodeOnline = &value
				break
			}
		}
		if !found {
			problem(w, 404, "node_not_found")
			return
		}
	}
	report, err := s.db.LocationStatistics(r.Context(), source, now, duration, day)
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	report.Location, report.NodeOnline = location, nodeOnline
	respond(w, 200, report)
}

func validNodeStatistics(observation *store.StatisticsObservation, now time.Time) bool {
	s := observation.Sample
	if s.At < now.Unix()-180 || s.At > now.Unix()+60 || len(observation.Counters) > 4096 || s.Cores < 0 || s.Cores > 65536 {
		return false
	}
	if s.CPU != nil && (math.IsNaN(*s.CPU) || math.IsInf(*s.CPU, 0) || *s.CPU < 0 || *s.CPU > 100) {
		return false
	}
	if s.Online != nil && (*s.Online < 0 || *s.Online > 1000000) {
		return false
	}
	if s.Connections != nil && (*s.Connections < 0 || *s.Connections > 10000000) {
		return false
	}
	if s.MemoryUsed != nil && s.MemoryTotal != nil && *s.MemoryUsed > *s.MemoryTotal {
		return false
	}
	if s.DiskUsed != nil && s.DiskTotal != nil && *s.DiskUsed > *s.DiskTotal {
		return false
	}
	return true
}

// TrackStatistics persists minute samples independently of any browser window.
func TrackStatistics(ctx context.Context, address, database string, db *store.Store) {
	metrics, err := endpoint.NewMetrics(address)
	if err != nil {
		log.Printf("statistics: %v", err)
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	host := &hoststats.Sampler{}
	failing := false
	for {
		sampleCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		sample := store.StatisticsSample{At: time.Now().Unix(), Resources: host.Read(filepath.Dir(database))}
		var counters map[string]store.Counter
		clients, metricsErr := metrics.Clients(sampleCtx)
		if metricsErr == nil {
			counters = map[string]store.Counter{}
			users, userErr := db.List(sampleCtx)
			if userErr == nil {
				known := map[string]bool{}
				for _, u := range users {
					known[u.Username] = true
				}
				seen := map[string]bool{}
				for _, c := range clients {
					old := counters[c.Username]
					old.Download += c.Outbound
					old.Upload += c.Inbound
					counters[c.Username] = old
					if c.Sessions > 0 && known[c.Username] {
						seen[c.Username] = true
					}
				}
				count := len(seen)
				sample.Online = &count
			} else {
				metricsErr = userErr
				counters = nil
			}
		}
		err := db.RecordStatistics(sampleCtx, sample, counters)
		cancel()
		if err == nil {
			err = metricsErr
		}
		if err != nil && !failing && ctx.Err() == nil {
			log.Printf("statistics: %v", err)
		}
		failing = err != nil
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
