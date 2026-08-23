package mint

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const testInviteKeyID = "mint-test-invite-key"

type generatedInvites struct {
	output       string
	manifestPath string
	manifest     inviteManifestFile
	books        []inviteBookFile
}

func generateInvites(t *testing.T, seats, vouchers int, start, end time.Time) generatedInvites {
	t.Helper()
	if err := invitePlatformCheck(); err != nil {
		t.Skip(err)
	}
	out := filepath.Join(t.TempDir(), "invite-output")
	if err := GenerateInviteBooks(InviteBookGenerationConfig{
		ProgramID:         "beta-test",
		MintKeyID:         testInviteKeyID,
		NotBefore:         start,
		NotAfter:          end,
		Seats:             seats,
		VouchersPerInvite: vouchers,
		OutputDir:         out,
	}); err != nil {
		t.Fatalf("GenerateInviteBooks: %v", err)
	}
	manifestPath := filepath.Join(out, "invite-manifest.json")
	var manifest inviteManifestFile
	readJSONFile(t, manifestPath, &manifest)
	books := make([]inviteBookFile, seats)
	for i := range seats {
		readJSONFile(t, filepath.Join(out, "books", fmt.Sprintf("invite-%03d.json", i+1)), &books[i])
	}
	return generatedInvites{output: out, manifestPath: manifestPath, manifest: manifest, books: books}
}

func readJSONFile(t *testing.T, path string, dst any) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("Unmarshal(%s): %v", path, err)
	}
	return data
}

func voucherFromBook(t *testing.T, book inviteBookFile, slot int) string {
	t.Helper()
	seed, err := base64.RawURLEncoding.Strict().DecodeString(book.Seed)
	if err != nil {
		t.Fatalf("decoding book seed: %v", err)
	}
	manifest := inviteManifestFile{
		ProgramID: book.ProgramID, MintKeyID: book.MintKeyID,
		NotBefore: book.NotBefore, NotAfter: book.NotAfter,
	}
	return base64.RawURLEncoding.EncodeToString(deriveInviteVoucher(seed, manifest, slot))
}

func TestInviteAuthorizerClaimsVoucherExactlyOnce(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	generated := generateInvites(t, 1, 2, start, start.Add(48*time.Hour))
	store := NewMemoryReceiptStore()
	auth, err := NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: generated.manifestPath,
		MintKeyID:    testInviteKeyID,
		Receipts:     store,
		Now:          func() time.Time { return start },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer: %v", err)
	}
	if auth.ProgramID() != "beta-test" || auth.Capacity() != 2 {
		t.Fatalf("metadata = program %q capacity %d", auth.ProgramID(), auth.Capacity())
	}
	gotStart, gotEnd := auth.Window()
	if !gotStart.Equal(start) || !gotEnd.Equal(start.Add(48*time.Hour)) {
		t.Fatalf("Window = [%s,%s)", gotStart, gotEnd)
	}

	first := []byte(voucherFromBook(t, generated.books[0], 0))
	if err := auth.Authorize(context.Background(), first); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	if err := auth.Authorize(context.Background(), first); !errors.Is(err, ErrReceiptUsed) {
		t.Fatalf("replay Authorize = %v, want ErrReceiptUsed", err)
	}
	second := []byte(voucherFromBook(t, generated.books[0], 1))
	if err := auth.Authorize(context.Background(), second); err != nil {
		t.Fatalf("second independent voucher: %v", err)
	}
}

func TestInviteVoucherIssuesOneBlindTokenOverHTTP(t *testing.T) {
	if err := invitePlatformCheck(); err != nil {
		t.Skip(err)
	}
	priv := key(t)
	keyID := KeyID(&priv.PublicKey)
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	out := filepath.Join(t.TempDir(), "invite-output")
	if err := GenerateInviteBooks(InviteBookGenerationConfig{
		ProgramID: "beta-http", MintKeyID: keyID, NotBefore: start, NotAfter: start.Add(time.Hour),
		Seats: 1, VouchersPerInvite: 1, OutputDir: out,
	}); err != nil {
		t.Fatalf("GenerateInviteBooks: %v", err)
	}
	var book inviteBookFile
	readJSONFile(t, filepath.Join(out, "books", "invite-001.json"), &book)
	auth, err := NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: filepath.Join(out, "invite-manifest.json"),
		MintKeyID:    keyID,
		Receipts:     NewMemoryReceiptStore(),
		Now:          func() time.Time { return start },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer: %v", err)
	}
	m, err := New(priv, auth)
	if err != nil {
		t.Fatalf("New mint: %v", err)
	}
	server := httptest.NewServer(NewServer(m, quietLog()).Handler())
	defer server.Close()
	client := &Client{URL: server.URL, ExpectKeyID: keyID}
	receipt := voucherFromBook(t, book, 0)
	token, err := client.Token(context.Background(), receipt)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if err := Verify(&priv.PublicKey, token); err != nil {
		t.Fatalf("Verify issued token: %v", err)
	}
	if _, err := client.Token(context.Background(), receipt); !errors.Is(err, ErrNotPaid) {
		t.Fatalf("replayed HTTP voucher = %v, want ErrNotPaid", err)
	}
}

func TestInviteAuthorizerConcurrentClaimsHaveOneWinner(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	generated := generateInvites(t, 1, 1, start, start.Add(time.Hour))
	auth, err := NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: generated.manifestPath, MintKeyID: testInviteKeyID,
		Receipts: NewMemoryReceiptStore(), Now: func() time.Time { return start },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer: %v", err)
	}
	voucher := []byte(voucherFromBook(t, generated.books[0], 0))

	const racers = 64
	results := make(chan error, racers)
	var wg sync.WaitGroup
	startRace := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startRace
			results <- auth.Authorize(context.Background(), voucher)
		}()
	}
	close(startRace)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrReceiptUsed):
		default:
			t.Fatalf("unexpected authorization error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent winners = %d, want 1", winners)
	}
}

func TestInviteAuthorizerClaimSurvivesRestart(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	generated := generateInvites(t, 1, 1, start, start.Add(time.Hour))
	dbPath := filepath.Join(t.TempDir(), "receipts.db")
	voucher := []byte(voucherFromBook(t, generated.books[0], 0))

	store, err := OpenFileReceiptStore(dbPath)
	if err != nil {
		t.Fatalf("OpenFileReceiptStore: %v", err)
	}
	auth, err := NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: generated.manifestPath, MintKeyID: testInviteKeyID,
		Receipts: store, Now: func() time.Time { return start },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer: %v", err)
	}
	if err := auth.Authorize(context.Background(), voucher); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = OpenFileReceiptStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	auth, err = NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: generated.manifestPath, MintKeyID: testInviteKeyID,
		Receipts: store, Now: func() time.Time { return start },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer after restart: %v", err)
	}
	if err := auth.Authorize(context.Background(), voucher); !errors.Is(err, ErrReceiptUsed) {
		t.Fatalf("Authorize after restart = %v, want ErrReceiptUsed", err)
	}
}

type countingReceiptStore struct {
	mu     sync.Mutex
	claims int
	inner  ReceiptStore
}

func (s *countingReceiptStore) Claim(ctx context.Context, rail string, receipt []byte) error {
	s.mu.Lock()
	s.claims++
	s.mu.Unlock()
	return s.inner.Claim(ctx, rail, receipt)
}

func (s *countingReceiptStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

func TestInviteAuthorizerChecksBeforeConsuming(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	generated := generateInvites(t, 1, 1, start, end)
	store := &countingReceiptStore{inner: NewMemoryReceiptStore()}
	now := start.Add(-time.Nanosecond)
	auth, err := NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: generated.manifestPath, MintKeyID: testInviteKeyID,
		Receipts: store, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer: %v", err)
	}
	valid := []byte(voucherFromBook(t, generated.books[0], 0))

	if err := auth.Authorize(context.Background(), valid); !errors.Is(err, ErrInviteWindowClosed) {
		t.Fatalf("before window = %v", err)
	}
	now = end
	if err := auth.Authorize(context.Background(), valid); !errors.Is(err, ErrInviteWindowClosed) {
		t.Fatalf("at exclusive end = %v", err)
	}
	now = start
	unknown := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xee}, inviteVoucherBytes))
	if err := auth.Authorize(context.Background(), []byte(unknown)); !errors.Is(err, ErrInviteVoucherInvalid) {
		t.Fatalf("unknown voucher = %v", err)
	}
	for _, malformed := range []string{"", " ", "abc", unknown + "=", "!" + unknown[1:], "\t" + unknown} {
		if err := auth.Authorize(context.Background(), []byte(malformed)); !errors.Is(err, ErrInviteVoucherInvalid) {
			t.Errorf("malformed %q = %v", malformed, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := auth.Authorize(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v", err)
	}
	if store.count() != 0 {
		t.Fatalf("pre-claim refusals called Claim %d times", store.count())
	}
	if err := auth.Authorize(context.Background(), valid); err != nil {
		t.Fatalf("valid voucher at inclusive start: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("successful authorization called Claim %d times", store.count())
	}
}

func TestMalformedBlindedInputDoesNotConsumeInviteVoucher(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	generated := generateInvites(t, 1, 1, start, start.Add(time.Hour))
	store := &countingReceiptStore{inner: NewMemoryReceiptStore()}
	auth, err := NewInviteAuthorizer(InviteAuthorizerConfig{
		ManifestPath: generated.manifestPath, MintKeyID: testInviteKeyID,
		Receipts: store, Now: func() time.Time { return start },
	})
	if err != nil {
		t.Fatalf("NewInviteAuthorizer: %v", err)
	}
	m, err := New(key(t), auth)
	if err != nil {
		t.Fatalf("New mint: %v", err)
	}
	voucher := []byte(voucherFromBook(t, generated.books[0], 0))
	if _, err := m.Issue(context.Background(), voucher, []byte{1}); err == nil {
		t.Fatal("malformed blinded input was accepted")
	}
	if store.count() != 0 {
		t.Fatalf("malformed blinded input consumed voucher through %d Claim calls", store.count())
	}
	blinding, err := Blind(m.PublicKey())
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}
	if _, err := m.Issue(context.Background(), voucher, blinding.Blinded); err != nil {
		t.Fatalf("voucher was not usable after malformed blinded input: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("valid issue called Claim %d times", store.count())
	}
}

func TestInviteManifestValidationFailsClosed(t *testing.T) {
	start := "2026-08-23T00:00:00Z"
	end := "2026-08-24T00:00:00Z"
	voucher := bytes.Repeat([]byte{0x42}, inviteVoucherBytes)
	fingerprint, err := receiptKey(inviteMembershipRail("beta-test", testInviteKeyID), voucher)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(fingerprint[:])
	base := inviteManifestFile{
		SchemaVersion: inviteSchemaVersion, ProgramID: "beta-test", MintKeyID: testInviteKeyID,
		NotBefore: start, NotAfter: end, Seats: 1, VouchersPerInvite: 1,
		Fingerprints: []string{encoded},
	}

	cases := map[string]func() []byte{
		"unknown field": func() []byte {
			data, _ := json.Marshal(base)
			return bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"person":"Ezra"`), 1)
		},
		"duplicate known field": func() []byte {
			data, _ := json.Marshal(base)
			return bytes.Replace(data, []byte(`"not_after":"2026-08-24T00:00:00Z"`),
				[]byte(`"not_after":"2026-08-24T00:00:00Z","not_after":"2099-01-01T00:00:00Z"`), 1)
		},
		"trailing json":   func() []byte { return append(mustMarshalJSON(base), []byte(` {}`)...) },
		"wrong schema":    func() []byte { v := base; v.SchemaVersion = 2; return mustMarshalJSON(v) },
		"bad program":     func() []byte { v := base; v.ProgramID = "person/email"; return mustMarshalJSON(v) },
		"wrong key":       func() []byte { v := base; v.MintKeyID = "mint-other"; return mustMarshalJSON(v) },
		"offset time":     func() []byte { v := base; v.NotBefore = "2026-08-22T19:00:00-05:00"; return mustMarshalJSON(v) },
		"fractional time": func() []byte { v := base; v.NotBefore = "2026-08-23T00:00:00.1Z"; return mustMarshalJSON(v) },
		"empty window":    func() []byte { v := base; v.NotAfter = v.NotBefore; return mustMarshalJSON(v) },
		"zero seats":      func() []byte { v := base; v.Seats = 0; return mustMarshalJSON(v) },
		"count mismatch":  func() []byte { v := base; v.VouchersPerInvite = 2; return mustMarshalJSON(v) },
		"bad fingerprint": func() []byte { v := base; v.Fingerprints = []string{"not-base64!"}; return mustMarshalJSON(v) },
		"duplicate fingerprint": func() []byte {
			v := base
			v.VouchersPerInvite = 2
			v.Fingerprints = []string{encoded, encoded}
			return mustMarshalJSON(v)
		},
		"unsorted fingerprints": func() []byte {
			otherVoucher := bytes.Repeat([]byte{0x24}, inviteVoucherBytes)
			other, _ := receiptKey(inviteMembershipRail("beta-test", testInviteKeyID), otherVoucher)
			v := base
			v.VouchersPerInvite = 2
			v.Fingerprints = []string{
				base64.RawURLEncoding.EncodeToString(other[:]),
				encoded,
			}
			sort.Sort(sort.Reverse(sort.StringSlice(v.Fingerprints)))
			return mustMarshalJSON(v)
		},
		"multiple values": func() []byte { return append(mustMarshalJSON(base), mustMarshalJSON(base)...) },
	}
	for name, makeData := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, makeData(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewInviteAuthorizer(InviteAuthorizerConfig{
				ManifestPath: path, MintKeyID: testInviteKeyID, Receipts: NewMemoryReceiptStore(),
			}); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}

	t.Run("non-regular file", func(t *testing.T) {
		if _, err := NewInviteAuthorizer(InviteAuthorizerConfig{
			ManifestPath: t.TempDir(), MintKeyID: testInviteKeyID, Receipts: NewMemoryReceiptStore(),
		}); err == nil {
			t.Fatal("directory accepted as manifest")
		}
	})
	if invitePlatformCheck() == nil {
		t.Run("writable manifest", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, mustMarshalJSON(base), 0o666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o666); err != nil {
				t.Fatal(err)
			}
			if _, err := NewInviteAuthorizer(InviteAuthorizerConfig{
				ManifestPath: path, MintKeyID: testInviteKeyID, Receipts: NewMemoryReceiptStore(),
			}); err == nil || !strings.Contains(err.Error(), "unsafe mode") {
				t.Fatalf("writable authorization manifest = %v", err)
			}
		})
		t.Run("writable manifest directory", func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "manifest.json")
			if err := os.WriteFile(path, mustMarshalJSON(base), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Fatal(err)
			}
			if _, err := NewInviteAuthorizer(InviteAuthorizerConfig{
				ManifestPath: path, MintKeyID: testInviteKeyID, Receipts: NewMemoryReceiptStore(),
			}); err == nil || !strings.Contains(err.Error(), "owner-controlled") {
				t.Fatalf("manifest in writable directory = %v", err)
			}
		})
	}
	t.Run("missing receipt store", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(path, mustMarshalJSON(base), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewInviteAuthorizer(InviteAuthorizerConfig{ManifestPath: path, MintKeyID: testInviteKeyID}); err == nil {
			t.Fatal("nil ReceiptStore accepted")
		}
	})
}

func TestGenerateInviteBooksProducesOnlyUngroupedManifestFingerprints(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	generated := generateInvites(t, 10, 10, start, start.Add(7*24*time.Hour))
	manifestData, err := os.ReadFile(generated.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.manifest.Fingerprints) != 100 {
		t.Fatalf("manifest fingerprints = %d, want 100", len(generated.manifest.Fingerprints))
	}
	if !sort.StringsAreSorted(generated.manifest.Fingerprints) {
		t.Fatal("manifest fingerprints retain generation order rather than an ungrouped canonical order")
	}
	ignore, err := os.ReadFile(filepath.Join(generated.output, ".gitignore"))
	if err != nil {
		t.Fatalf("reading generated .gitignore: %v", err)
	}
	if !bytes.Contains(ignore, []byte("*")) {
		t.Fatalf("generated output lacks a deny-by-default .gitignore: %q", ignore)
	}

	want := make(map[string]struct{}, 100)
	for _, book := range generated.books {
		if book.VoucherCount != 10 {
			t.Fatalf("book VoucherCount = %d", book.VoucherCount)
		}
		if bytes.Contains(manifestData, []byte(book.Seed)) {
			t.Fatal("redeemable invite seed leaked into mint manifest")
		}
		for slot := 0; slot < book.VoucherCount; slot++ {
			encodedVoucher := voucherFromBook(t, book, slot)
			if bytes.Contains(manifestData, []byte(encodedVoucher)) {
				t.Fatal("redeemable voucher leaked into mint manifest")
			}
			raw, _ := base64.RawURLEncoding.Strict().DecodeString(encodedVoucher)
			fingerprint, err := receiptKey(inviteMembershipRail(book.ProgramID, book.MintKeyID), raw)
			if err != nil {
				t.Fatal(err)
			}
			want[base64.RawURLEncoding.EncodeToString(fingerprint[:])] = struct{}{}
		}
	}
	if len(want) != 100 {
		t.Fatalf("derived unique fingerprints = %d, want 100", len(want))
	}
	for _, got := range generated.manifest.Fingerprints {
		if _, ok := want[got]; !ok {
			t.Fatalf("manifest fingerprint %q was not derived from a generated book", got)
		}
	}

	for _, path := range []string{generated.output, filepath.Join(generated.output, "books")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("directory %s mode = %04o, want owner-only", path, info.Mode().Perm())
		}
	}
	paths := []string{generated.manifestPath, filepath.Join(generated.output, "books", "invite-001.json")}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("file %s mode = %04o, want owner-only", path, info.Mode().Perm())
		}
	}
}

func TestInviteManifestAndClaimFingerprintsUseSeparateDomains(t *testing.T) {
	voucher := bytes.Repeat([]byte{0x7a}, inviteVoucherBytes)
	membership, err := receiptKey(inviteMembershipRail("beta-test", testInviteKeyID), voucher)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := receiptKey(inviteClaimRail("beta-test", testInviteKeyID), voucher)
	if err != nil {
		t.Fatal(err)
	}
	if membership == claim {
		t.Fatal("manifest membership fingerprint is identical to durable claim key")
	}
}

func TestGenerateInviteBooksRefusesOverwrite(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	generated := generateInvites(t, 1, 1, start, start.Add(time.Hour))
	before, err := os.ReadFile(generated.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	err = GenerateInviteBooks(InviteBookGenerationConfig{
		ProgramID: "other", MintKeyID: testInviteKeyID, NotBefore: start, NotAfter: start.Add(time.Hour),
		Seats: 1, VouchersPerInvite: 1, OutputDir: generated.output,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second generation = %v", err)
	}
	after, err := os.ReadFile(generated.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused generation still changed existing manifest")
	}
}

type failedRandom struct{}

func (failedRandom) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func TestGenerateInviteBooksWritesManifestLast(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	out := filepath.Join(t.TempDir(), "failed-output")
	err := GenerateInviteBooks(InviteBookGenerationConfig{
		ProgramID: "beta-test", MintKeyID: testInviteKeyID, NotBefore: start, NotAfter: start.Add(time.Hour),
		Seats: 1, VouchersPerInvite: 1, OutputDir: out, random: failedRandom{},
	})
	if err == nil {
		t.Fatal("generation succeeded without entropy")
	}
	if _, statErr := os.Stat(filepath.Join(out, "invite-manifest.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed generation left a usable manifest: %v", statErr)
	}
}

func TestGenerateInviteBooksRejectsWritableOutputParent(t *testing.T) {
	if err := invitePlatformCheck(); err != nil {
		t.Skip(err)
	}
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(parent, "invite-output")
	err := GenerateInviteBooks(InviteBookGenerationConfig{
		ProgramID: "beta-test", MintKeyID: testInviteKeyID, NotBefore: start, NotAfter: start.Add(time.Hour),
		Seats: 1, VouchersPerInvite: 1, OutputDir: out,
	})
	if err == nil || !strings.Contains(err.Error(), "owner-controlled") {
		t.Fatalf("generation under writable parent = %v", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsafe output parent still received generated files: %v", statErr)
	}
}

func TestGenerateInviteBooksRejectsDuplicateVoucherMaterial(t *testing.T) {
	if err := invitePlatformCheck(); err != nil {
		t.Skip(err)
	}
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	out := filepath.Join(t.TempDir(), "duplicate-output")
	err := GenerateInviteBooks(InviteBookGenerationConfig{
		ProgramID: "beta-test", MintKeyID: testInviteKeyID, NotBefore: start, NotAfter: start.Add(time.Hour),
		Seats: 2, VouchersPerInvite: 1, OutputDir: out,
		random: bytes.NewReader(make([]byte, 2*inviteSeedBytes)),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate voucher") {
		t.Fatalf("duplicate generation = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "invite-manifest.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("duplicate generation left a usable manifest: %v", statErr)
	}
}

func TestInviteGenerationValidation(t *testing.T) {
	start := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	valid := InviteBookGenerationConfig{
		ProgramID: "beta-test", MintKeyID: testInviteKeyID,
		NotBefore: start, NotAfter: start.Add(time.Hour),
		Seats: 1, VouchersPerInvite: 1,
	}
	cases := map[string]func(*InviteBookGenerationConfig){
		"missing output":  func(c *InviteBookGenerationConfig) {},
		"invalid program": func(c *InviteBookGenerationConfig) { c.ProgramID = "invite/person" },
		"missing key":     func(c *InviteBookGenerationConfig) { c.MintKeyID = "" },
		"inverted window": func(c *InviteBookGenerationConfig) { c.NotAfter = c.NotBefore.Add(-time.Second) },
		"fractional time": func(c *InviteBookGenerationConfig) { c.NotBefore = c.NotBefore.Add(time.Nanosecond) },
		"zero seats":      func(c *InviteBookGenerationConfig) { c.Seats = 0 },
		"zero vouchers":   func(c *InviteBookGenerationConfig) { c.VouchersPerInvite = 0 },
		"too many":        func(c *InviteBookGenerationConfig) { c.Seats = maxInviteVouchers; c.VouchersPerInvite = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			cfg.OutputDir = filepath.Join(t.TempDir(), "out")
			mutate(&cfg)
			if name == "missing output" {
				cfg.OutputDir = ""
			}
			if err := GenerateInviteBooks(cfg); err == nil {
				t.Fatal("invalid generator configuration was accepted")
			}
		})
	}
}

func TestInviteModeFailsClosedOnUnsupportedPlatforms(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Skip("invite mode is supported on this platform")
	}
	want := "supported only on Linux and macOS"
	if _, err := NewInviteAuthorizer(InviteAuthorizerConfig{}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewInviteAuthorizer error = %v, want unsupported-platform refusal", err)
	}
	if err := GenerateInviteBooks(InviteBookGenerationConfig{}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("GenerateInviteBooks error = %v, want unsupported-platform refusal", err)
	}
}
