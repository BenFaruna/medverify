package auth

import "context"

// ChainStore tries each store in order and returns the first key that resolves.
// It lets a deployment accept database-backed keys and, in development, the
// legacy single key without either mode hiding the other.
type ChainStore struct {
	stores []Store
}

// NewChainStore builds a store from the given stores. Nil stores are ignored,
// so a caller can assemble a chain from optional parts without branching.
func NewChainStore(stores ...Store) *ChainStore {
	kept := make([]Store, 0, len(stores))
	for _, store := range stores {
		if store != nil {
			kept = append(kept, store)
		}
	}
	return &ChainStore{stores: kept}
}

// Lookup returns the first successful lookup. A rejection from one store does
// not stop the chain: the token may belong to a later store. Every failure is
// reported as ErrUnknownClient so the caller cannot tell which store rejected
// the token, or whether it was rejected at all.
func (s *ChainStore) Lookup(ctx context.Context, accessToken string) (*Key, error) {
	if s == nil {
		return nil, ErrUnknownClient
	}
	for _, store := range s.stores {
		key, err := store.Lookup(ctx, accessToken)
		if err == nil && key != nil {
			return key, nil
		}
	}
	return nil, ErrUnknownClient
}
