package usage

import (
	"context"
	"time"
)

// APIUsage is the persisted usage payload shape grouped by API bucket and model.
type APIUsage map[string]map[string][]RequestDetail

// QueryRange constrains persisted usage queries to a UTC time window.
type QueryRange struct {
	Start *time.Time
	End   *time.Time
}

// Store persists usage records for later querying.
type Store interface {
	Insert(ctx context.Context, record PersistedRecord) error
	Query(ctx context.Context, rng QueryRange) (APIUsage, error)
	Close() error
}

// PersistedRecord is the storage form of a usage event.
type PersistedRecord struct {
	Timestamp          time.Time
	APIKey             string
	Provider           string
	Model              string
	Source             string
	AuthIndex          string
	AuthType           string
	Endpoint           string
	RequestID          string
	LatencyMs          int64
	FirstByteLatencyMs int64
	GenerationMs       int64
	ThinkingEffort     string
	Tokens             TokenStats
	Failed             bool
}
