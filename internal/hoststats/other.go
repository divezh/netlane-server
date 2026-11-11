//go:build !linux

package hoststats

import "github.com/divezh/netlane-server/internal/store"

type Sampler struct{}

// Unsupported platforms report missing host values, never fabricated zero usage.
func (s *Sampler) Read(path string) store.Resources { return store.Resources{} }
