package auth

import (
	"context"
	"errors"
	"testing"
)

type stubStore struct {
	key Key
	err error
}

func (s stubStore) Lookup(context.Context, string) (*Key, error) {
	if s.err != nil {
		return nil, s.err
	}
	key := s.key
	return &key, nil
}

func TestChainStoreAcceptsAKeyFromAnyStore(t *testing.T) {
	t.Parallel()

	chain := NewChainStore(
		stubStore{err: ErrUnknownClient},
		stubStore{key: Key{ClientID: 42, ClientName: "acme"}},
	)

	key, err := chain.Lookup(context.Background(), "mv_abc_secret")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if key.ClientID != 42 {
		t.Fatalf("ClientID = %d, want 42", key.ClientID)
	}
}

func TestChainStorePrefersTheEarlierStore(t *testing.T) {
	t.Parallel()

	chain := NewChainStore(
		stubStore{key: Key{ClientID: 1}},
		stubStore{key: Key{ClientID: 2}},
	)

	key, err := chain.Lookup(context.Background(), "mv_abc_secret")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if key.ClientID != 1 {
		t.Fatalf("ClientID = %d, want the first store's 1", key.ClientID)
	}
}

func TestChainStoreRejectsWhenNoStoreAccepts(t *testing.T) {
	t.Parallel()

	chain := NewChainStore(
		stubStore{err: ErrUnknownClient},
		stubStore{err: ErrUnknownClient},
	)

	if _, err := chain.Lookup(context.Background(), "mv_abc_secret"); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("error = %v, want ErrUnknownClient", err)
	}
}

func TestChainStoreIgnoresNilStoresAndNilChain(t *testing.T) {
	t.Parallel()

	chain := NewChainStore(nil, stubStore{key: Key{ClientID: 7}}, nil)
	key, err := chain.Lookup(context.Background(), "mv_abc_secret")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if key.ClientID != 7 {
		t.Fatalf("ClientID = %d, want 7", key.ClientID)
	}

	var missing *ChainStore
	if _, err := missing.Lookup(context.Background(), "mv_abc_secret"); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("nil chain error = %v, want ErrUnknownClient", err)
	}
}
