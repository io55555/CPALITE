package auth

import "context"

// Store abstracts persistence of Auth state across restarts.
type Store interface {
	// List returns all auth records stored in the backend.
	List(ctx context.Context) ([]*Auth, error)
	// Save persists the provided auth record, replacing any existing one with same ID.
	Save(ctx context.Context, auth *Auth) (string, error)
	// Delete removes the auth record identified by id.
	Delete(ctx context.Context, id string) error
}

// LightweightAuthProvider 提供不含敏感 payload 的轻量认证快照。
type LightweightAuthProvider interface {
	ListLightweight(ctx context.Context) ([]*Auth, error)
}

// AuthHydrator 按需从持久化层恢复完整认证对象。
type AuthHydrator interface {
	HydrateAuth(ctx context.Context, id string) (*Auth, error)
}
