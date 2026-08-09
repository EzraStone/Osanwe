package mint

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrReceiptUsed = errors.New("mint: payment receipt has already been used")

var receiptsBucket = []byte("used-receipts-v1")

// ReceiptStore atomically consumes one opaque payment entitlement. The rail is
// a namespace, so two payment systems may use the same receipt bytes safely.
type ReceiptStore interface {
	Claim(context.Context, string, []byte) error
}

// MemoryReceiptStore is a process-local ReceiptStore for tests.
type MemoryReceiptStore struct {
	mu   sync.Mutex
	used map[[sha256.Size]byte]struct{}
}

func NewMemoryReceiptStore() *MemoryReceiptStore {
	return &MemoryReceiptStore{used: make(map[[sha256.Size]byte]struct{})}
}

func (s *MemoryReceiptStore) Claim(ctx context.Context, rail string, receipt []byte) error {
	key, err := receiptKey(rail, receipt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.used[key]; exists {
		return ErrReceiptUsed
	}
	s.used[key] = struct{}{}
	return nil
}

// FileReceiptStore is a durable single-process ReceiptStore backed by bbolt.
// It stores only a domain-separated SHA-256 fingerprint, never the invoice ID
// itself. The database is still sensitive because its growth reveals issuance
// volume and timing, so it is created mode 0600.
type FileReceiptStore struct {
	db *bolt.DB
}

func OpenFileReceiptStore(path string) (*FileReceiptStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("mint: used-receipt database path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("mint: resolving used-receipt database path: %w", err)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("mint: used-receipt database %s is not a regular file", absPath)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("mint: used-receipt database %s has unsafe mode %04o; group and world must have no access", absPath, info.Mode().Perm())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("mint: inspecting used-receipt database %s: %w", absPath, err)
	}
	db, err := bolt.Open(absPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("mint: opening used-receipt database %s: %w", absPath, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(receiptsBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mint: initializing used-receipt database %s: %w", absPath, err)
	}
	return &FileReceiptStore{db: db}, nil
}

func (s *FileReceiptStore) Claim(ctx context.Context, rail string, receipt []byte) error {
	key, err := receiptKey(rail, receipt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(receiptsBucket)
		if bucket.Get(key[:]) != nil {
			return ErrReceiptUsed
		}
		return bucket.Put(key[:], []byte{1})
	})
}

func (s *FileReceiptStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func receiptKey(rail string, receipt []byte) ([sha256.Size]byte, error) {
	if rail == "" || len(rail) > 255 {
		return [sha256.Size]byte{}, errors.New("mint: receipt rail namespace is required and must fit in 255 bytes")
	}
	if len(receipt) == 0 || len(receipt) > MaxIssueBody {
		return [sha256.Size]byte{}, errors.New("mint: receipt is empty or too large")
	}
	data := make([]byte, 4+len(rail)+len(receipt))
	binary.BigEndian.PutUint32(data[:4], uint32(len(rail)))
	copy(data[4:], rail)
	copy(data[4+len(rail):], receipt)
	return sha256.Sum256(data), nil
}

var _ ReceiptStore = (*MemoryReceiptStore)(nil)
var _ ReceiptStore = (*FileReceiptStore)(nil)
