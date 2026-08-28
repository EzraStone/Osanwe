package mint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	inviteWalletSchemaVersion = 1
	maxInviteBookSize         = 64 << 10
)

var (
	ErrInviteActivationRequired = errors.New("mint: activate free test access with an invite book first")
	ErrInviteEpochExhausted     = errors.New("mint: this invite book has used its allowance for the current epoch")
)

var (
	inviteWalletBucket = []byte("invite-wallet-v1")
	inviteWalletKey    = []byte("state")
)

// InviteWalletConfig configures a crash-safe local free-beta wallet. StatePath
// contains secret invitation material and unspent bearer tokens; callers must
// place it inside the current user's private application-data directory.
type InviteWalletConfig struct {
	Client    *Client
	StatePath string
	Batch     int
	Now       func() time.Time
}

// InviteWalletStatus contains only safe counts and public time boundaries.
// It deliberately excludes voucher material, token material and the book id.
type InviteWalletStatus struct {
	Activated      bool
	RemainingEpoch int
	EpochEnds      time.Time
	Expires        time.Time
}

// InviteWallet reserves a one-shot voucher durably before asking the mint for
// a blind signature. A crash or ambiguous network failure can lose one free
// voucher, but can never retry it with a different blinded message.
type InviteWallet struct {
	client   *Client
	db       *bolt.DB
	batch    int
	lowWater int
	now      func() time.Time
	refill   chan struct{}
}

type inviteWalletRecord struct {
	SchemaVersion int            `json:"schema_version"`
	BookID        string         `json:"book_id"`
	Book          inviteBookFile `json:"book"`
	Reserved      []bool         `json:"reserved"`
	Tokens        []string       `json:"tokens"`
	Spent         uint64         `json:"spent"`
}

// OpenInviteWallet opens one exclusive local wallet database. bbolt's file
// lock prevents two client processes from spending the same invitation state.
func OpenInviteWallet(cfg InviteWalletConfig) (*InviteWallet, error) {
	if cfg.Client == nil {
		return nil, errors.New("mint: invite wallet requires a mint client")
	}
	if strings.TrimSpace(cfg.StatePath) == "" {
		return nil, errors.New("mint: invite wallet state path is required")
	}
	abs, err := filepath.Abs(cfg.StatePath)
	if err != nil {
		return nil, fmt.Errorf("mint: resolving invite wallet state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("mint: creating invite wallet directory: %w", err)
	}
	if os.PathSeparator == '/' {
		parent, err := os.Lstat(filepath.Dir(abs))
		if err != nil || !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("mint: invite wallet directory must be owner-only")
		}
		if existing, err := os.Lstat(abs); err == nil {
			if !existing.Mode().IsRegular() || existing.Mode().Perm()&0o077 != 0 {
				return nil, errors.New("mint: existing invite wallet must be a regular owner-only file")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("mint: inspecting invite wallet: %w", err)
		}
	}
	db, err := bolt.Open(abs, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("mint: opening invite wallet (is another Osanwë client running?): %w", err)
	}
	if err := os.Chmod(abs, 0o600); err != nil && os.PathSeparator == '/' {
		_ = db.Close()
		return nil, fmt.Errorf("mint: securing invite wallet: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(inviteWalletBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mint: initializing invite wallet: %w", err)
	}
	batch := cfg.Batch
	if batch < 1 {
		batch = 5
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &InviteWallet{
		client: cfg.Client, db: db, batch: batch, lowWater: batch / 2,
		now: now, refill: make(chan struct{}, 1),
	}, nil
}

// Close releases the exclusive wallet lock.
func (w *InviteWallet) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}

// ActivateInviteBook validates and imports one invitation. Re-importing the
// same book is idempotent; replacing an activated book is refused so a web
// page cannot silently discard tokens or quota state.
func (w *InviteWallet) ActivateInviteBook(data []byte) error {
	if w == nil || w.db == nil || w.client == nil {
		return errors.New("mint: invite wallet is not configured")
	}
	book, seed, err := parseInviteBook(data, w.client.ExpectKeyID)
	if err != nil {
		return err
	}
	bookID := inviteBookID(book, seed)
	err = w.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(inviteWalletBucket)
		if raw := bucket.Get(inviteWalletKey); raw != nil {
			record, err := decodeInviteWalletRecord(raw)
			if err != nil {
				return err
			}
			if record.BookID != bookID {
				return errors.New("mint: a different invite book is already activated in this wallet")
			}
			return nil
		}
		record := inviteWalletRecord{
			SchemaVersion: inviteWalletSchemaVersion,
			BookID:        bookID, Book: book, Reserved: make([]bool, book.VoucherCount),
		}
		return putInviteWalletRecord(bucket, record)
	})
	if err == nil {
		w.triggerRefill()
	}
	return err
}

// Take returns one token and persists its removal before handing it to the
// caller. If no token is stocked, one current-epoch voucher is reserved before
// the mint request begins.
func (w *InviteWallet) Take(ctx context.Context) (*Token, error) {
	if tok, ok, err := w.takeStocked(); err != nil {
		return nil, err
	} else if ok {
		if w.Len() <= w.lowWater {
			w.triggerRefill()
		}
		return tok, nil
	}
	tok, err := w.buy(ctx)
	if err != nil {
		return nil, err
	}
	if err := w.incrementSpent(); err != nil {
		return nil, err
	}
	w.triggerRefill()
	return tok, nil
}

// Put restores a token only when the bearer proves it was not presented or
// the authenticated gateway explicitly marks it reusable.
func (w *InviteWallet) Put(tok *Token) {
	if w == nil || w.db == nil || tok == nil {
		return
	}
	_ = w.db.Update(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if err != nil {
			return err
		}
		record.Tokens = append(record.Tokens, tok.Encode())
		if record.Spent > 0 {
			record.Spent--
		}
		return putInviteWalletRecord(tx.Bucket(inviteWalletBucket), record)
	})
}

func (w *InviteWallet) Len() int {
	if w == nil || w.db == nil {
		return 0
	}
	count := 0
	_ = w.db.View(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if errors.Is(err, ErrInviteActivationRequired) {
			return nil
		}
		if err != nil {
			return err
		}
		count = len(record.Tokens)
		return nil
	})
	return count
}

func (w *InviteWallet) Spent() uint64 {
	if w == nil || w.db == nil {
		return 0
	}
	var spent uint64
	_ = w.db.View(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if errors.Is(err, ErrInviteActivationRequired) {
			return nil
		}
		if err == nil {
			spent = record.Spent
		}
		return err
	})
	return spent
}

func (w *InviteWallet) InviteStatus() InviteWalletStatus {
	status := InviteWalletStatus{}
	if w == nil || w.db == nil {
		return status
	}
	_ = w.db.View(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if errors.Is(err, ErrInviteActivationRequired) {
			return nil
		}
		if err != nil {
			return err
		}
		status.Activated = true
		status.Expires = mustParseInviteTime(record.Book.NotAfter)
		now := w.now().UTC()
		start, end, first, last, ok := inviteBookCurrentEpoch(record.Book, now)
		_ = start
		if ok {
			status.EpochEnds = end
			for i := first; i < last; i++ {
				if !record.Reserved[i] {
					status.RemainingEpoch++
				}
			}
		}
		return nil
	})
	return status
}

func (w *InviteWallet) Run(ctx context.Context) {
	w.fill(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.refill:
			w.fill(ctx)
		}
	}
}

func (w *InviteWallet) triggerRefill() {
	select {
	case w.refill <- struct{}{}:
	default:
	}
}

func (w *InviteWallet) fill(ctx context.Context) {
	for w.Len() < w.batch && ctx.Err() == nil {
		tok, err := w.buy(ctx)
		if err != nil {
			return
		}
		if err := w.stock(tok); err != nil {
			return
		}
	}
}

func (w *InviteWallet) buy(ctx context.Context) (*Token, error) {
	voucher, err := w.reserveVoucher(w.now().UTC())
	if err != nil {
		return nil, err
	}
	return w.client.Token(ctx, voucher)
}

func (w *InviteWallet) reserveVoucher(now time.Time) (string, error) {
	var voucher string
	err := w.db.Update(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if err != nil {
			return err
		}
		_, _, first, last, ok := inviteBookCurrentEpoch(record.Book, now)
		if !ok {
			return ErrInviteWindowClosed
		}
		slot := -1
		for i := first; i < last; i++ {
			if !record.Reserved[i] {
				slot = i
				break
			}
		}
		if slot < 0 {
			return ErrInviteEpochExhausted
		}
		seed, _ := base64.RawURLEncoding.Strict().DecodeString(record.Book.Seed)
		manifest := inviteManifestFile{
			ProgramID: record.Book.ProgramID, MintKeyID: record.Book.MintKeyID,
			NotBefore: record.Book.NotBefore, NotAfter: record.Book.NotAfter,
		}
		voucher = base64.RawURLEncoding.EncodeToString(deriveInviteVoucher(seed, manifest, slot))
		record.Reserved[slot] = true
		return putInviteWalletRecord(tx.Bucket(inviteWalletBucket), record)
	})
	return voucher, err
}

func (w *InviteWallet) takeStocked() (*Token, bool, error) {
	var encoded string
	err := w.db.Update(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if err != nil {
			return err
		}
		if len(record.Tokens) == 0 {
			return nil
		}
		encoded = record.Tokens[len(record.Tokens)-1]
		record.Tokens = record.Tokens[:len(record.Tokens)-1]
		record.Spent++
		return putInviteWalletRecord(tx.Bucket(inviteWalletBucket), record)
	})
	if err != nil || encoded == "" {
		return nil, false, err
	}
	tok, err := ParseToken(encoded)
	return tok, err == nil, err
}

func (w *InviteWallet) stock(tok *Token) error {
	return w.db.Update(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if err != nil {
			return err
		}
		record.Tokens = append(record.Tokens, tok.Encode())
		return putInviteWalletRecord(tx.Bucket(inviteWalletBucket), record)
	})
}

func (w *InviteWallet) incrementSpent() error {
	return w.db.Update(func(tx *bolt.Tx) error {
		record, err := loadInviteWalletRecord(tx.Bucket(inviteWalletBucket))
		if err != nil {
			return err
		}
		record.Spent++
		return putInviteWalletRecord(tx.Bucket(inviteWalletBucket), record)
	})
}

func parseInviteBook(data []byte, expectedMintKeyID string) (inviteBookFile, []byte, error) {
	var book inviteBookFile
	if len(data) == 0 || len(data) > maxInviteBookSize {
		return book, nil, fmt.Errorf("mint: invite book must be between 1 and %d bytes", maxInviteBookSize)
	}
	dec := json.NewDecoder(io.LimitReader(bytes.NewReader(data), maxInviteBookSize+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&book); err != nil {
		return book, nil, fmt.Errorf("mint: parsing invite book: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return book, nil, errors.New("mint: invite book contains trailing data")
	}
	if book.SchemaVersion != inviteSchemaVersion && book.SchemaVersion != dailyInviteSchemaVersion {
		return book, nil, fmt.Errorf("mint: invite book schema_version is %d, want %d or %d", book.SchemaVersion, inviteSchemaVersion, dailyInviteSchemaVersion)
	}
	if !inviteProgramPattern.MatchString(book.ProgramID) || book.MintKeyID == "" || book.MintKeyID != expectedMintKeyID {
		return book, nil, errors.New("mint: invite book does not match this beta mint")
	}
	start, err := parseInviteTime(book.NotBefore)
	if err != nil {
		return book, nil, fmt.Errorf("mint: invite book not_before: %w", err)
	}
	end, err := parseInviteTime(book.NotAfter)
	if err != nil || !end.After(start) {
		return book, nil, errors.New("mint: invite book has an invalid issuance window")
	}
	if book.VoucherCount < 1 || book.VoucherCount > maxInviteVouchers {
		return book, nil, errors.New("mint: invite book voucher_count is invalid")
	}
	seed, err := base64.RawURLEncoding.Strict().DecodeString(book.Seed)
	if err != nil || len(seed) != inviteSeedBytes || base64.RawURLEncoding.EncodeToString(seed) != book.Seed {
		return book, nil, errors.New("mint: invite book seed is malformed")
	}
	if book.SchemaVersion == inviteSchemaVersion {
		if book.VouchersPerEpoch != 0 || len(book.Epochs) != 0 {
			return book, nil, errors.New("mint: fixed-window invite book contains epoch fields")
		}
		return book, seed, nil
	}
	if book.VouchersPerEpoch < 1 || book.VoucherCount%book.VouchersPerEpoch != 0 || len(book.Epochs) != book.VoucherCount/book.VouchersPerEpoch {
		return book, nil, errors.New("mint: invite book epoch counts are inconsistent")
	}
	previous := start
	for i, epoch := range book.Epochs {
		epochStart, err := parseInviteTime(epoch.NotBefore)
		if err != nil {
			return book, nil, fmt.Errorf("mint: invite book epoch %d is malformed", i)
		}
		epochEnd, err := parseInviteTime(epoch.NotAfter)
		if err != nil || !epochStart.Equal(previous) || !epochEnd.After(epochStart) || epochEnd.After(end) {
			return book, nil, fmt.Errorf("mint: invite book epoch %d is not a contiguous part of the issuance window", i)
		}
		previous = epochEnd
	}
	if !previous.Equal(end) {
		return book, nil, errors.New("mint: invite book epochs do not cover the issuance window")
	}
	return book, seed, nil
}

func inviteBookCurrentEpoch(book inviteBookFile, now time.Time) (time.Time, time.Time, int, int, bool) {
	if book.SchemaVersion == inviteSchemaVersion {
		start, end := mustParseInviteTime(book.NotBefore), mustParseInviteTime(book.NotAfter)
		return start, end, 0, book.VoucherCount, !now.Before(start) && now.Before(end)
	}
	for i, epoch := range book.Epochs {
		start, end := mustParseInviteTime(epoch.NotBefore), mustParseInviteTime(epoch.NotAfter)
		if !now.Before(start) && now.Before(end) {
			first := i * book.VouchersPerEpoch
			return start, end, first, first + book.VouchersPerEpoch, true
		}
	}
	return time.Time{}, time.Time{}, 0, 0, false
}

func inviteBookID(book inviteBookFile, seed []byte) string {
	encoded, _ := json.Marshal(book)
	h := sha256.New()
	h.Write([]byte("osanwe-invite-book-v1"))
	h.Write(encoded)
	h.Write(seed)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func loadInviteWalletRecord(bucket *bolt.Bucket) (inviteWalletRecord, error) {
	if bucket == nil || bucket.Get(inviteWalletKey) == nil {
		return inviteWalletRecord{}, ErrInviteActivationRequired
	}
	return decodeInviteWalletRecord(bucket.Get(inviteWalletKey))
}

func decodeInviteWalletRecord(data []byte) (inviteWalletRecord, error) {
	var record inviteWalletRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("mint: invite wallet state is corrupt: %w", err)
	}
	if record.SchemaVersion != inviteWalletSchemaVersion || record.BookID == "" || len(record.Reserved) != record.Book.VoucherCount {
		return record, errors.New("mint: invite wallet state is inconsistent")
	}
	for _, encoded := range record.Tokens {
		if _, err := ParseToken(encoded); err != nil {
			return record, errors.New("mint: invite wallet contains an invalid token")
		}
	}
	return record, nil
}

func putInviteWalletRecord(bucket *bolt.Bucket, record inviteWalletRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return bucket.Put(inviteWalletKey, encoded)
}
