package gateway

import (
	"context"
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

var ErrBudgetExhausted = errors.New("gateway: aggregate budget exhausted")

const (
	DefaultBudgetWindow       = time.Hour
	DefaultBudgetRequests     = uint64(100)
	DefaultBudgetInputBytes   = uint64(10 << 20)
	DefaultBudgetOutputTokens = uint64(100_000)
)

var budgetBucket = []byte("aggregate-budget-v2")
var budgetCurrentKey = []byte("current")

// BudgetRequest is the maximum provider work a request is asking the gateway
// to buy. MaxOutputTokens is reserved, rather than guessed from the eventual
// response, so concurrent requests cannot collectively cross the hard ceiling.
type BudgetRequest struct {
	Model           string
	InputBytes      int
	MaxOutputTokens int
}

// BudgetReservation represents capacity held for one request. Release is used
// only when the provider was provably never reached. Once dispatch may have
// happened, the reservation remains charged even if the provider fails.
type BudgetReservation interface {
	Release(context.Context) error
}

// Budget atomically reserves aggregate provider capacity.
type Budget interface {
	Reserve(context.Context, BudgetRequest) (BudgetReservation, error)
}

// UnlimitedBudget is an explicit escape hatch for tests and local demos. A
// production command never selects it implicitly.
type UnlimitedBudget struct{}

func (UnlimitedBudget) Reserve(context.Context, BudgetRequest) (BudgetReservation, error) {
	return unlimitedReservation{}, nil
}

type unlimitedReservation struct{}

func (unlimitedReservation) Release(context.Context) error { return nil }

// BudgetLimitError reports when capacity becomes available again.
type BudgetLimitError struct {
	Reset time.Time
}

func (e *BudgetLimitError) Error() string {
	return fmt.Sprintf("%v; resets at %s", ErrBudgetExhausted, e.Reset.UTC().Format(time.RFC3339))
}

func (e *BudgetLimitError) Unwrap() error { return ErrBudgetExhausted }

// FileBudgetConfig configures a single-node durable aggregate budget.
type FileBudgetConfig struct {
	Path            string
	Window          time.Duration
	MaxRequests     uint64
	MaxInputBytes   uint64
	MaxOutputTokens uint64

	// Now is overridable for tests.
	Now func() time.Time
}

// FileBudget stores one fixed-window counter in an ACID bbolt database. A
// process crash after reservation can conservatively overcount until the
// window resets; it cannot cause the provider limit to be exceeded.
type FileBudget struct {
	db              *bolt.DB
	window          time.Duration
	maxRequests     uint64
	maxInputBytes   uint64
	maxOutputTokens uint64
	now             func() time.Time
}

// BudgetUsage is a diagnostic snapshot. It contains no request, token, model,
// or client identifiers.
type BudgetUsage struct {
	WindowStart     time.Time
	WindowEnd       time.Time
	Requests        uint64
	InputBytes      uint64
	OutputTokens    uint64
	MaxRequests     uint64
	MaxInputBytes   uint64
	MaxOutputTokens uint64
}

// OpenFileBudget opens a durable single-node budget database. The database
// must not be placed on NFS and cannot be shared by multiple gateway hosts.
func OpenFileBudget(cfg FileBudgetConfig) (*FileBudget, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("gateway: budget database path is required")
	}
	if cfg.Window == 0 {
		cfg.Window = DefaultBudgetWindow
	}
	if cfg.Window < time.Minute || cfg.Window > 24*time.Hour || cfg.Window%time.Second != 0 {
		return nil, errors.New("gateway: budget window must be a whole number of seconds between one minute and 24 hours")
	}
	if cfg.MaxRequests == 0 {
		cfg.MaxRequests = DefaultBudgetRequests
	}
	if cfg.MaxInputBytes == 0 {
		cfg.MaxInputBytes = DefaultBudgetInputBytes
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = DefaultBudgetOutputTokens
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	absPath, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("gateway: resolving budget database path: %w", err)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("gateway: budget database %s is not a regular file", absPath)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("gateway: budget database %s has unsafe mode %04o; group and world must have no access", absPath, info.Mode().Perm())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("gateway: inspecting budget database %s: %w", absPath, err)
	}

	db, err := bolt.Open(absPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("gateway: opening budget database %s: %w", absPath, err)
	}
	b := &FileBudget{
		db: db, window: cfg.Window, maxRequests: cfg.MaxRequests, maxInputBytes: cfg.MaxInputBytes,
		maxOutputTokens: cfg.MaxOutputTokens, now: cfg.Now,
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(budgetBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("gateway: initializing budget database %s: %w", absPath, err)
	}
	return b, nil
}

func (b *FileBudget) Reserve(ctx context.Context, req BudgetRequest) (BudgetReservation, error) {
	if req.InputBytes < 1 || req.MaxOutputTokens < 1 {
		return nil, errors.New("gateway: budget reservation requires positive input bytes and max output tokens")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := b.now()
	start, end := b.bounds(now)
	input := uint64(req.InputBytes)
	tokens := uint64(req.MaxOutputTokens)

	err := b.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(budgetBucket)
		windowStart, requests, inputBytes, outputTokens, err := decodeBudget(bucket.Get(budgetCurrentKey))
		if err != nil {
			return err
		}
		if windowStart != start.Unix() {
			requests, inputBytes, outputTokens = 0, 0, 0
		}
		if requests >= b.maxRequests || input > b.maxInputBytes || inputBytes > b.maxInputBytes-input ||
			tokens > b.maxOutputTokens || outputTokens > b.maxOutputTokens-tokens {
			return &BudgetLimitError{Reset: end}
		}
		return bucket.Put(budgetCurrentKey, encodeBudget(start.Unix(), requests+1, inputBytes+input, outputTokens+tokens))
	})
	if err != nil {
		return nil, err
	}
	return &fileBudgetReservation{budget: b, windowStart: start.Unix(), inputBytes: input, outputTokens: tokens}, nil
}

func (b *FileBudget) Usage() (BudgetUsage, error) {
	now := b.now()
	start, end := b.bounds(now)
	usage := BudgetUsage{
		WindowStart: start, WindowEnd: end,
		MaxRequests: b.maxRequests, MaxInputBytes: b.maxInputBytes, MaxOutputTokens: b.maxOutputTokens,
	}
	err := b.db.View(func(tx *bolt.Tx) error {
		windowStart, requests, inputBytes, outputTokens, err := decodeBudget(tx.Bucket(budgetBucket).Get(budgetCurrentKey))
		if err != nil {
			return err
		}
		if windowStart == start.Unix() {
			usage.Requests, usage.InputBytes, usage.OutputTokens = requests, inputBytes, outputTokens
		}
		return nil
	})
	return usage, err
}

func (b *FileBudget) Close() error {
	if b == nil || b.db == nil {
		return nil
	}
	return b.db.Close()
}

func (b *FileBudget) bounds(now time.Time) (time.Time, time.Time) {
	seconds := int64(b.window / time.Second)
	start := time.Unix(now.Unix()/seconds*seconds, 0).UTC()
	return start, start.Add(b.window)
}

type fileBudgetReservation struct {
	mu           sync.Mutex
	budget       *FileBudget
	windowStart  int64
	inputBytes   uint64
	outputTokens uint64
	released     bool
}

func (r *fileBudgetReservation) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.budget.db.Update(func(tx *bolt.Tx) error {
		windowStart, requests, inputBytes, outputTokens, err := decodeBudget(tx.Bucket(budgetBucket).Get(budgetCurrentKey))
		if err != nil {
			return err
		}
		if windowStart != r.windowStart {
			return nil
		}
		if requests == 0 || inputBytes < r.inputBytes || outputTokens < r.outputTokens {
			return errors.New("gateway: budget database contains counters smaller than the reservation being released")
		}
		return tx.Bucket(budgetBucket).Put(budgetCurrentKey,
			encodeBudget(windowStart, requests-1, inputBytes-r.inputBytes, outputTokens-r.outputTokens))
	})
	if err == nil {
		r.released = true
	}
	return err
}

func encodeBudget(windowStart int64, requests, inputBytes, outputTokens uint64) []byte {
	data := make([]byte, 32)
	binary.BigEndian.PutUint64(data[0:8], uint64(windowStart))
	binary.BigEndian.PutUint64(data[8:16], requests)
	binary.BigEndian.PutUint64(data[16:24], inputBytes)
	binary.BigEndian.PutUint64(data[24:32], outputTokens)
	return data
}

func decodeBudget(data []byte) (int64, uint64, uint64, uint64, error) {
	if len(data) == 0 {
		return 0, 0, 0, 0, nil
	}
	if len(data) != 32 {
		return 0, 0, 0, 0, errors.New("gateway: budget database contains a corrupt counter record")
	}
	return int64(binary.BigEndian.Uint64(data[0:8])),
		binary.BigEndian.Uint64(data[8:16]), binary.BigEndian.Uint64(data[16:24]),
		binary.BigEndian.Uint64(data[24:32]), nil
}

var _ Budget = UnlimitedBudget{}
var _ Budget = (*FileBudget)(nil)
