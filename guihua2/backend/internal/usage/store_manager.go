package usage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var (
	defaultStoreMu sync.RWMutex
	defaultStore   Store
)

func InitDefaultStore(path string) error {
	store, err := NewSQLiteStore(path)
	if err != nil {
		return err
	}
	replaceDefaultStore(store)
	return nil
}

func DefaultStore() Store {
	defaultStoreMu.RLock()
	defer defaultStoreMu.RUnlock()
	return defaultStore
}

func CloseDefaultStore() error {
	return replaceDefaultStore(nil)
}

func SetDefaultStoreForTest(store Store) func() {
	previous := DefaultStore()
	defaultStoreMu.Lock()
	defaultStore = store
	defaultStoreMu.Unlock()
	return func() {
		_ = replaceDefaultStore(previous)
	}
}

func replaceDefaultStore(store Store) error {
	defaultStoreMu.Lock()
	previous := defaultStore
	defaultStore = store
	defaultStoreMu.Unlock()
	if previous == nil {
		return nil
	}
	return previous.Close()
}

func SnapshotRange(ctx context.Context, rng QueryRange) (StatisticsSnapshot, error) {
	if store := DefaultStore(); store != nil {
		usageData, err := store.Query(ctx, rng)
		if err != nil {
			return StatisticsSnapshot{}, err
		}
		return snapshotFromAPIUsage(usageData), nil
	}
	if rng.Start != nil || rng.End != nil {
		return StatisticsSnapshot{}, fmt.Errorf("usage store unavailable for ranged query")
	}
	return GetRequestStatistics().Snapshot(), nil
}

func ImportSnapshot(ctx context.Context, snapshot StatisticsSnapshot) (MergeResult, error) {
	if store := DefaultStore(); store != nil {
		return importSnapshotToStore(ctx, store, snapshot)
	}
	return GetRequestStatistics().MergeSnapshot(snapshot), nil
}

func snapshotFromAPIUsage(data APIUsage) StatisticsSnapshot {
	snapshot := StatisticsSnapshot{
		APIs:           make(map[string]APISnapshot, len(data)),
		RequestsByDay:  map[string]int64{},
		RequestsByHour: map[string]int64{},
		TokensByDay:    map[string]int64{},
		TokensByHour:   map[string]int64{},
	}
	for apiName, models := range data {
		apiSnapshot := APISnapshot{
			Models: make(map[string]ModelSnapshot, len(models)),
		}
		for modelName, details := range models {
			copied := make([]RequestDetail, 0, len(details))
			var modelRequests int64
			var modelTokens int64
			for _, detail := range details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				copied = append(copied, detail)
				modelRequests++
				modelTokens += detail.Tokens.TotalTokens
				snapshot.TotalRequests++
				if detail.Failed {
					snapshot.FailureCount++
				} else {
					snapshot.SuccessCount++
				}
				snapshot.TotalTokens += detail.Tokens.TotalTokens
				dayKey := detail.Timestamp.Format("2006-01-02")
				hourKey := formatHour(detail.Timestamp.Hour())
				snapshot.RequestsByDay[dayKey]++
				snapshot.RequestsByHour[hourKey]++
				snapshot.TokensByDay[dayKey] += detail.Tokens.TotalTokens
				snapshot.TokensByHour[hourKey] += detail.Tokens.TotalTokens
			}
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelRequests,
				TotalTokens:   modelTokens,
				Details:       copied,
			}
			apiSnapshot.TotalRequests += modelRequests
			apiSnapshot.TotalTokens += modelTokens
		}
		snapshot.APIs[apiName] = apiSnapshot
	}
	return snapshot
}

func importSnapshotToStore(ctx context.Context, store Store, snapshot StatisticsSnapshot) (MergeResult, error) {
	result := MergeResult{}
	if store == nil {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for apiName, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now().UTC()
				}
				err := store.Insert(ctx, PersistedRecord{
					Timestamp:          detail.Timestamp.UTC(),
					APIKey:             apiName,
					Model:              modelName,
					Source:             detail.Source,
					AuthIndex:          detail.AuthIndex,
					RequestID:          detail.RequestID,
					LatencyMs:          detail.LatencyMs,
					FirstByteLatencyMs: detail.FirstByteLatencyMs,
					GenerationMs:       detail.GenerationMs,
					ThinkingEffort:     detail.ThinkingEffort,
					Tokens:             detail.Tokens,
					Failed:             detail.Failed,
				})
				if err != nil {
					return result, err
				}
				result.Added++
			}
		}
	}
	return result, nil
}
