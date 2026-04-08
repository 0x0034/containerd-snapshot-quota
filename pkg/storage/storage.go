package storage

import (
	"encoding/json"
	"fmt"
	"strings"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/0x0034/containerd-snapshot-quota/pkg/model"
)

// Store wraps a Badger DB for quota persistence.
type Store struct {
	db *badger.DB
}

// New opens a Badger database at the given directory.
func New(dir string) (*Store, error) {
	opts := badger.DefaultOptions(dir).
		WithLoggingLevel(badger.WARNING)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open badger db at %s: %w", dir, err)
	}
	return &Store{db: db}, nil
}

// Close shuts down the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// RunGC triggers a Badger value log GC cycle.
func (s *Store) RunGC() error {
	return s.db.RunValueLogGC(0.5)
}

// SetQuota persists a quota record.
func (s *Store) SetQuota(key string, q model.Quota) error {
	data, err := json.Marshal(q)
	if err != nil {
		return fmt.Errorf("marshal quota: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), data)
	})
}

// GetQuota retrieves a quota by key.
func (s *Store) GetQuota(key string) (model.Quota, error) {
	var q model.Quota
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &q)
		})
	})
	return q, err
}

// DeleteQuota removes a quota record.
func (s *Store) DeleteQuota(key string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// ListQuotas returns all quotas whose key starts with the given prefix.
func (s *Store) ListQuotas(prefix string) ([]model.Quota, error) {
	var quotas []model.Quota
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte(prefix)); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())
			if !strings.HasPrefix(key, prefix) {
				break
			}
			var q model.Quota
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &q)
			}); err != nil {
				continue
			}
			quotas = append(quotas, q)
		}
		return nil
	})
	return quotas, err
}

// CleanAll removes all quota records with the given prefix.
func (s *Store) CleanAll(prefix string) error {
	quotas, err := s.ListQuotas(prefix)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		for _, q := range quotas {
			if err := txn.Delete([]byte(prefix + q.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}
