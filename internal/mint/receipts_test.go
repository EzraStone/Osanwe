package mint

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestMemoryReceiptStoreClaimsExactlyOnce(t *testing.T) {
	s := NewMemoryReceiptStore()
	if err := s.Claim(context.Background(), "btcpay", []byte("invoice")); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if err := s.Claim(context.Background(), "btcpay", []byte("invoice")); !errors.Is(err, ErrReceiptUsed) {
		t.Fatalf("second Claim = %v, want ErrReceiptUsed", err)
	}
	if err := s.Claim(context.Background(), "another-rail", []byte("invoice")); err != nil {
		t.Fatalf("same receipt in a distinct namespace: %v", err)
	}
}

func TestFileReceiptStoreIsAtomicAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.db")
	s, err := OpenFileReceiptStore(path)
	if err != nil {
		t.Fatalf("OpenFileReceiptStore: %v", err)
	}

	const attempts = 30
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Claim(context.Background(), "btcpay", []byte("invoice"))
		}()
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrReceiptUsed) {
			t.Fatalf("unexpected Claim error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = OpenFileReceiptStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if err := s.Claim(context.Background(), "btcpay", []byte("invoice")); !errors.Is(err, ErrReceiptUsed) {
		t.Fatalf("Claim after restart = %v, want ErrReceiptUsed", err)
	}
}

func TestReceiptStoreHonoursCancellation(t *testing.T) {
	s := NewMemoryReceiptStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Claim(ctx, "btcpay", []byte("invoice")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Claim = %v, want context.Canceled", err)
	}
	if err := s.Claim(context.Background(), "btcpay", []byte("invoice")); err != nil {
		t.Fatalf("cancelled claim consumed receipt: %v", err)
	}
}
