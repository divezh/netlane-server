package api

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

// PresenceInterval is how often the panel samples who is online. The endpoint keeps no
// history, so "last seen" is accurate to about this interval and has gaps while the panel is down.
const PresenceInterval = 15 * time.Second

// TrackPresence samples the endpoint's per-client metrics until ctx ends and records
// when each user was last online and from which address.
func TrackPresence(ctx context.Context, metricsAddress string, db *store.Store) {
	metrics, err := endpoint.NewMetrics(metricsAddress)
	if err != nil {
		log.Printf("presence: disabled: %v", err)
		return
	}
	ticker := time.NewTicker(PresenceInterval)
	defer ticker.Stop()
	failing := false
	for {
		sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := samplePresence(sampleCtx, metrics, db, time.Now())
		cancel()
		// Log a failure once, not every 15 seconds.
		if err != nil && !failing && ctx.Err() == nil {
			log.Printf("presence: %v", err)
		} else if err == nil && failing {
			log.Printf("presence: metrics available again")
		}
		failing = err != nil
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func samplePresence(ctx context.Context, metrics *endpoint.Metrics, db *store.Store, at time.Time) error {
	clients, err := metrics.Clients(ctx)
	if err != nil {
		return err
	}
	var seen []store.Seen
	for _, c := range clients {
		if c.Sessions == 0 {
			continue
		}
		item := store.Seen{Username: c.Username}
		if c.IP != nil {
			if ip := net.ParseIP(*c.IP); ip != nil {
				item.IP = ip.String()
			}
		}
		seen = append(seen, item)
	}
	return db.RecordPresence(ctx, at, seen)
}
