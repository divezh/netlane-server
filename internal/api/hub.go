package api

import (
	"context"
	"sync"
	"time"
)

// Hub wakes nodes waiting in a long poll when users change, and carries main's current
// name and country (they can change while the panel runs).
type Hub struct {
	mu            sync.Mutex
	changed       chan struct{}
	name, country string
}

func NewHub() *Hub { return &Hub{changed: make(chan struct{})} }

// Ticket is taken before checking the state; Wait then returns at once if Notify came after it.
func (h *Hub) Ticket() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.changed)
	h.changed = make(chan struct{})
}

// Wait reports whether a change came after the ticket was taken, within d.
func (h *Hub) Wait(ctx context.Context, ticket <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ticket:
		return true
	case <-timer.C:
	case <-ctx.Done():
	}
	return false
}

func (h *Hub) SetIdentity(name, country string) {
	h.mu.Lock()
	h.name, h.country = name, country
	h.mu.Unlock()
}

func (h *Hub) Identity() (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.name, h.country
}
