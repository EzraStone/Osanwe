package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBudgetDecoderPreservesPreCostCounters(t *testing.T) {
	legacy := make([]byte, 32)
	binary.BigEndian.PutUint64(legacy[0:8], 123)
	binary.BigEndian.PutUint64(legacy[8:16], 4)
	binary.BigEndian.PutUint64(legacy[16:24], 5)
	binary.BigEndian.PutUint64(legacy[24:32], 6)
	start, requests, input, output, cost, err := decodeBudget(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if start != 123 || requests != 4 || input != 5 || output != 6 || cost != 0 {
		t.Fatalf("decoded legacy record = %d, %d, %d, %d, %d", start, requests, input, output, cost)
	}
}

func openTestBudget(t *testing.T, now *time.Time, requests, tokens uint64) *FileBudget {
	t.Helper()
	b, err := OpenFileBudget(FileBudgetConfig{
		Path: filepath.Join(t.TempDir(), "budget.db"), Window: time.Hour,
		MaxRequests: requests, MaxInputBytes: tokens, MaxOutputTokens: tokens, Now: func() time.Time { return *now },
	})
	if err != nil {
		t.Fatalf("OpenFileBudget: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestFileBudgetRejectsInputVolumeIndependently(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b, err := OpenFileBudget(FileBudgetConfig{
		Path: filepath.Join(t.TempDir(), "budget.db"), Window: time.Hour,
		MaxRequests: 100, MaxInputBytes: 5, MaxOutputTokens: 100,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("OpenFileBudget: %v", err)
	}
	defer b.Close()
	_, err = b.Reserve(context.Background(), BudgetRequest{Model: "demo", InputBytes: 6, MaxOutputTokens: 1})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Reserve = %v, want ErrBudgetExhausted", err)
	}
}

func TestFileBudgetEnforcesCostIndependently(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b, err := OpenFileBudget(FileBudgetConfig{
		Path: filepath.Join(t.TempDir(), "budget.db"), Window: time.Hour,
		MaxRequests: 100, MaxInputBytes: 100, MaxOutputTokens: 100, MaxCostMicros: 5,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("OpenFileBudget: %v", err)
	}
	defer b.Close()
	if _, err := b.Reserve(context.Background(), BudgetRequest{
		Model: "demo", InputBytes: 1, MaxOutputTokens: 1, CostMicros: 4,
	}); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	_, err = b.Reserve(context.Background(), BudgetRequest{
		Model: "demo", InputBytes: 1, MaxOutputTokens: 1, CostMicros: 2,
	})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second Reserve = %v, want ErrBudgetExhausted", err)
	}
}

func TestCostAwareBudgetRejectsAnUnpricedReservation(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b, err := OpenFileBudget(FileBudgetConfig{
		Path: filepath.Join(t.TempDir(), "budget.db"), Window: time.Hour,
		MaxRequests: 1, MaxInputBytes: 1, MaxOutputTokens: 1, MaxCostMicros: 1,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("OpenFileBudget: %v", err)
	}
	defer b.Close()
	if _, err := b.Reserve(context.Background(), BudgetRequest{
		Model: "unpriced", InputBytes: 1, MaxOutputTokens: 1,
	}); err == nil {
		t.Fatal("cost-aware budget accepted an unpriced reservation")
	}
}

func TestFileBudgetEnforcesBothAggregateCeilings(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b := openTestBudget(t, &now, 2, 10)
	ctx := context.Background()

	if _, err := b.Reserve(ctx, BudgetRequest{Model: "small", InputBytes: 4, MaxOutputTokens: 4}); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	if _, err := b.Reserve(ctx, BudgetRequest{Model: "large", InputBytes: 6, MaxOutputTokens: 6}); err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	_, err := b.Reserve(ctx, BudgetRequest{Model: "small", InputBytes: 1, MaxOutputTokens: 1})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("third Reserve error = %v, want ErrBudgetExhausted", err)
	}
	var limit *BudgetLimitError
	if !errors.As(err, &limit) || !limit.Reset.Equal(time.Date(2026, 8, 9, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("limit reset = %v, want next fixed-window boundary", limit)
	}
}

func TestFileBudgetReleaseRestoresCapacityExactlyOnce(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b := openTestBudget(t, &now, 1, 10)
	ctx := context.Background()

	r, err := b.Reserve(ctx, BudgetRequest{Model: "demo", InputBytes: 10, MaxOutputTokens: 10, CostMicros: 7})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := r.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := r.Release(ctx); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if _, err := b.Reserve(ctx, BudgetRequest{Model: "demo", InputBytes: 10, MaxOutputTokens: 10, CostMicros: 7}); err != nil {
		t.Fatalf("Reserve after release: %v", err)
	}
	usage, err := b.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Requests != 1 || usage.InputBytes != 10 || usage.OutputTokens != 10 || usage.CostMicros != 7 {
		t.Fatalf("usage = %+v, want one request, ten input bytes, ten output tokens, and seven cost micros", usage)
	}
}

func TestFileBudgetReservationsAreAtomicUnderConcurrency(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b := openTestBudget(t, &now, 5, 5)

	const attempts = 40
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Reserve(context.Background(), BudgetRequest{Model: "demo", InputBytes: 1, MaxOutputTokens: 1})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	accepted := 0
	for err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrBudgetExhausted):
		default:
			t.Fatalf("unexpected Reserve error: %v", err)
		}
	}
	if accepted != 5 {
		t.Fatalf("accepted = %d, want exactly 5", accepted)
	}
}

func TestFileBudgetSurvivesRestartAndResetsOnSchedule(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "budget.db")
	cfg := FileBudgetConfig{
		Path: path, Window: time.Hour, MaxRequests: 1, MaxOutputTokens: 4,
		Now: func() time.Time { return now },
	}
	b, err := OpenFileBudget(cfg)
	if err != nil {
		t.Fatalf("first OpenFileBudget: %v", err)
	}
	if _, err := b.Reserve(context.Background(), BudgetRequest{Model: "demo", InputBytes: 4, MaxOutputTokens: 4}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err = OpenFileBudget(cfg)
	if err != nil {
		t.Fatalf("second OpenFileBudget: %v", err)
	}
	defer b.Close()
	if _, err := b.Reserve(context.Background(), BudgetRequest{Model: "demo", InputBytes: 1, MaxOutputTokens: 1}); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Reserve after restart = %v, want ErrBudgetExhausted", err)
	}
	now = now.Add(time.Hour)
	if _, err := b.Reserve(context.Background(), BudgetRequest{Model: "demo", InputBytes: 4, MaxOutputTokens: 4}); err != nil {
		t.Fatalf("Reserve in next window: %v", err)
	}
}

func TestFileBudgetHonoursCancellationBeforeMutation(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 15, 0, 0, time.UTC)
	b := openTestBudget(t, &now, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Reserve(ctx, BudgetRequest{Model: "demo", InputBytes: 1, MaxOutputTokens: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reserve error = %v, want context.Canceled", err)
	}
	usage, err := b.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Requests != 0 {
		t.Fatalf("cancelled reservation mutated usage: %+v", usage)
	}
}
