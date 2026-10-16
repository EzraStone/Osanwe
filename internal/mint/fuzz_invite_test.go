package mint

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// An invitation book is a file a tester was sent. Whatever it contains,
// parsing must not panic. Anything parseInviteBook accepts is later read with
// mustParseInviteTime, which panics on a bad time, so acceptance must imply
// that every later read of the book is safe and its voucher arithmetic sound.
func FuzzParseInviteBook(f *testing.F) {
	const mintKeyID = "mint-fuzz"
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seed := base64.RawURLEncoding.EncodeToString(make([]byte, inviteSeedBytes))
	daily := inviteBookFile{
		SchemaVersion: dailyInviteSchemaVersion, ProgramID: "fuzz", MintKeyID: mintKeyID,
		NotBefore: start.Format(inviteTimeFormat), NotAfter: start.Add(48 * time.Hour).Format(inviteTimeFormat),
		VoucherCount: 4, Seed: seed, VouchersPerEpoch: 2,
		Epochs: []inviteBookEpochFile{
			{NotBefore: start.Format(inviteTimeFormat), NotAfter: start.Add(24 * time.Hour).Format(inviteTimeFormat)},
			{NotBefore: start.Add(24 * time.Hour).Format(inviteTimeFormat), NotAfter: start.Add(48 * time.Hour).Format(inviteTimeFormat)},
		},
	}
	fixed := inviteBookFile{
		SchemaVersion: inviteSchemaVersion, ProgramID: "fuzz", MintKeyID: mintKeyID,
		NotBefore: start.Format(inviteTimeFormat), NotAfter: start.Add(48 * time.Hour).Format(inviteTimeFormat),
		VoucherCount: 3, Seed: seed,
	}
	for _, book := range []inviteBookFile{daily, fixed} {
		data, err := json.Marshal(book)
		if err != nil {
			f.Fatal(err)
		}
		if _, _, err := parseInviteBook(data, mintKeyID); err != nil {
			f.Fatalf("seed book does not parse: %v", err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":2,"epochs":[{"not_before":"x","not_after":"y"}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		book, seed, err := parseInviteBook(data, mintKeyID)
		if err != nil {
			return
		}
		if len(seed) != inviteSeedBytes || book.VoucherCount < 1 || book.VoucherCount > maxInviteVouchers {
			t.Fatalf("accepted a book with %d seed bytes and %d vouchers\n%q", len(seed), book.VoucherCount, data)
		}
		bookStart, bookEnd := mustParseInviteTime(book.NotBefore), mustParseInviteTime(book.NotAfter)
		probes := []time.Time{bookStart.Add(-time.Second), bookStart, bookEnd.Add(-time.Second), bookEnd}
		for _, epoch := range book.Epochs {
			probes = append(probes, mustParseInviteTime(epoch.NotBefore), mustParseInviteTime(epoch.NotAfter).Add(-time.Second))
		}
		for _, now := range probes {
			_, _, first, last, ok := inviteBookCurrentEpoch(book, now)
			if !ok {
				continue
			}
			if first < 0 || last > book.VoucherCount || first >= last {
				t.Fatalf("at %s the current epoch claims vouchers [%d, %d) of %d\n%q", now, first, last, book.VoucherCount, data)
			}
		}
	})
}
