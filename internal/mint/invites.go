package mint

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	inviteSchemaVersion   = 1
	inviteSeedBytes       = 32
	inviteVoucherBytes    = 32
	maxInviteManifestSize = 8 << 20
	maxInviteVouchers     = 100_000
	inviteTimeFormat      = "2006-01-02T15:04:05Z"
)

var (
	// ErrInviteVoucherInvalid is returned for a malformed or unknown beta
	// voucher. It deliberately does not distinguish the two cases.
	ErrInviteVoucherInvalid = errors.New("mint: invite voucher is not valid")

	// ErrInviteWindowClosed is returned outside the manifest's fixed UTC
	// issuance window. The end is exclusive.
	ErrInviteWindowClosed = errors.New("mint: invite issuance window is closed")

	inviteProgramPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// InviteAuthorizerConfig configures the explicitly enabled free-beta
// authorizer. The manifest contains only unordered voucher fingerprints; it
// must not contain invitee identifiers or redeemable voucher material.
type InviteAuthorizerConfig struct {
	ManifestPath string
	MintKeyID    string
	Receipts     ReceiptStore

	// Now is optional and exists so exact window boundaries can be tested.
	Now func() time.Time
}

// InviteAuthorizer consumes one pre-generated, unlinkable voucher per token.
// It learns whether a voucher belongs to this beta but has no identifier with
// which to group two vouchers as coming from the same invite book.
type InviteAuthorizer struct {
	programID      string
	mintKeyID      string
	notBefore      time.Time
	notAfter       time.Time
	membershipRail string
	claimRail      string
	valid          map[[sha256.Size]byte]struct{}
	receipts       ReceiptStore
	now            func() time.Time
}

type inviteManifestFile struct {
	SchemaVersion     int      `json:"schema_version"`
	ProgramID         string   `json:"program_id"`
	MintKeyID         string   `json:"mint_key_id"`
	NotBefore         string   `json:"not_before"`
	NotAfter          string   `json:"not_after"`
	Seats             int      `json:"seats"`
	VouchersPerInvite int      `json:"vouchers_per_invite"`
	Fingerprints      []string `json:"voucher_fingerprints"`
}

type inviteBookFile struct {
	SchemaVersion int    `json:"schema_version"`
	ProgramID     string `json:"program_id"`
	MintKeyID     string `json:"mint_key_id"`
	NotBefore     string `json:"not_before"`
	NotAfter      string `json:"not_after"`
	VoucherCount  int    `json:"voucher_count"`
	Seed          string `json:"seed"`
}

// NewInviteAuthorizer loads and validates a fixed-window invite manifest.
// Loading is fail-closed: unknown fields, trailing JSON, wrong mint keys,
// duplicate fingerprints, and inconsistent capacity all prevent startup.
func NewInviteAuthorizer(cfg InviteAuthorizerConfig) (*InviteAuthorizer, error) {
	if err := invitePlatformCheck(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ManifestPath) == "" {
		return nil, errors.New("mint: invite manifest path is required")
	}
	if cfg.MintKeyID == "" {
		return nil, errors.New("mint: invite manifest requires the loaded mint key id")
	}
	if cfg.Receipts == nil {
		return nil, errors.New("mint: invite vouchers require a durable receipt store")
	}

	data, err := readRegularFileBounded(cfg.ManifestPath, maxInviteManifestSize)
	if err != nil {
		return nil, fmt.Errorf("mint: reading invite manifest: %w", err)
	}
	manifest, err := parseInviteManifest(data, cfg.MintKeyID)
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	membershipRail := inviteMembershipRail(manifest.ProgramID, manifest.MintKeyID)
	valid := make(map[[sha256.Size]byte]struct{}, len(manifest.Fingerprints))
	for _, encoded := range manifest.Fingerprints {
		raw, _ := base64.RawURLEncoding.Strict().DecodeString(encoded)
		var fingerprint [sha256.Size]byte
		copy(fingerprint[:], raw)
		valid[fingerprint] = struct{}{}
	}

	return &InviteAuthorizer{
		programID:      manifest.ProgramID,
		mintKeyID:      manifest.MintKeyID,
		notBefore:      mustParseInviteTime(manifest.NotBefore),
		notAfter:       mustParseInviteTime(manifest.NotAfter),
		membershipRail: membershipRail,
		claimRail:      inviteClaimRail(manifest.ProgramID, manifest.MintKeyID),
		valid:          valid,
		receipts:       cfg.Receipts,
		now:            now,
	}, nil
}

// Authorize consumes one valid voucher. Window and membership checks happen
// before Claim, so malformed, unknown, early, and expired requests cannot burn
// a real voucher. Once Claim succeeds it is intentionally never rolled back:
// if the signing response is lost, fail-closed behavior costs one free voucher
// instead of risking two issued tokens.
func (a *InviteAuthorizer) Authorize(ctx context.Context, receipt []byte) error {
	if a == nil || a.receipts == nil || a.now == nil {
		return errors.New("mint: invite authorizer is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	voucher, err := decodeInviteVoucher(receipt)
	if err != nil {
		return ErrInviteVoucherInvalid
	}
	now := a.now().UTC()
	if now.Before(a.notBefore) || !now.Before(a.notAfter) {
		return ErrInviteWindowClosed
	}
	fingerprint, err := receiptKey(a.membershipRail, voucher)
	if err != nil {
		return ErrInviteVoucherInvalid
	}
	if _, ok := a.valid[fingerprint]; !ok {
		return ErrInviteVoucherInvalid
	}
	return a.receipts.Claim(ctx, a.claimRail, voucher)
}

// ProgramID is safe aggregate metadata for operator logs.
func (a *InviteAuthorizer) ProgramID() string { return a.programID }

// Capacity is the fixed maximum number of tokens represented by the manifest.
func (a *InviteAuthorizer) Capacity() int { return len(a.valid) }

// Window reports the half-open UTC issuance window.
func (a *InviteAuthorizer) Window() (time.Time, time.Time) {
	return a.notBefore, a.notAfter
}

// InviteBookGenerationConfig describes one offline generation run. OutputDir
// must not exist: generation refuses to merge with or overwrite prior secret
// material.
type InviteBookGenerationConfig struct {
	ProgramID         string
	MintKeyID         string
	NotBefore         time.Time
	NotAfter          time.Time
	Seats             int
	VouchersPerInvite int
	OutputDir         string

	// random is deliberately not exported: callers cannot accidentally replace
	// the CSPRNG. Package tests inject deterministic failures through it.
	random io.Reader
}

// GenerateInviteBooks creates an operator manifest and one secret seed book
// per invite. The manifest is written last and contains only a sorted,
// ungrouped set of fingerprints, so its order cannot reveal which vouchers
// came from one book. All output is created with owner-only permissions.
func GenerateInviteBooks(cfg InviteBookGenerationConfig) error {
	if err := invitePlatformCheck(); err != nil {
		return err
	}
	manifest, err := validateInviteGeneration(cfg)
	if err != nil {
		return err
	}

	out, err := filepath.Abs(cfg.OutputDir)
	if err != nil {
		return fmt.Errorf("mint: resolving invite output directory: %w", err)
	}
	parent := filepath.Dir(out)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("mint: inspecting invite output parent: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 || !inviteFileOwnedByTrustedUser(parentInfo) {
		return fmt.Errorf("mint: invite output parent %s must be an owner-controlled, non-writable-by-others directory", parent)
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("mint: invite output %s already exists; refusing to overwrite or merge secret books", out)
		}
		return fmt.Errorf("mint: creating invite output %s: %w", out, err)
	}
	if err := writeFileExclusive(filepath.Join(out, ".gitignore"), []byte("*\n!.gitignore\n"), 0o600); err != nil {
		return fmt.Errorf("mint: writing invite output safety marker (output is incomplete): %w", err)
	}
	booksDir := filepath.Join(out, "books")
	if err := os.Mkdir(booksDir, 0o700); err != nil {
		return fmt.Errorf("mint: creating invite books directory (output is incomplete): %w", err)
	}

	random := cfg.random
	if random == nil {
		random = rand.Reader
	}
	seen := make(map[[sha256.Size]byte]struct{}, cfg.Seats*cfg.VouchersPerInvite)
	for seat := 1; seat <= cfg.Seats; seat++ {
		seed := make([]byte, inviteSeedBytes)
		if _, err := io.ReadFull(random, seed); err != nil {
			return fmt.Errorf("mint: reading randomness for invite book %d (output is incomplete): %w", seat, err)
		}
		for slot := 0; slot < cfg.VouchersPerInvite; slot++ {
			voucher := deriveInviteVoucher(seed, manifest, slot)
			fingerprint, err := receiptKey(inviteMembershipRail(cfg.ProgramID, cfg.MintKeyID), voucher)
			if err != nil {
				return fmt.Errorf("mint: fingerprinting invite voucher: %w", err)
			}
			if _, duplicate := seen[fingerprint]; duplicate {
				return errors.New("mint: invite randomness produced a duplicate voucher; output is incomplete and must not be used")
			}
			seen[fingerprint] = struct{}{}
			manifest.Fingerprints = append(manifest.Fingerprints,
				base64.RawURLEncoding.EncodeToString(fingerprint[:]))
		}

		book := inviteBookFile{
			SchemaVersion: inviteSchemaVersion,
			ProgramID:     manifest.ProgramID,
			MintKeyID:     manifest.MintKeyID,
			NotBefore:     manifest.NotBefore,
			NotAfter:      manifest.NotAfter,
			VoucherCount:  manifest.VouchersPerInvite,
			Seed:          base64.RawURLEncoding.EncodeToString(seed),
		}
		path := filepath.Join(booksDir, fmt.Sprintf("invite-%03d.json", seat))
		if err := writeJSONExclusive(path, book, 0o600); err != nil {
			return fmt.Errorf("mint: writing invite book %d (output is incomplete): %w", seat, err)
		}
	}

	sort.Strings(manifest.Fingerprints)
	if _, err := parseInviteManifest(mustMarshalJSON(manifest), cfg.MintKeyID); err != nil {
		return fmt.Errorf("mint: generated an invalid invite manifest (output is incomplete): %w", err)
	}
	if err := writeJSONExclusive(filepath.Join(out, "invite-manifest.json"), manifest, 0o600); err != nil {
		return fmt.Errorf("mint: writing invite manifest (output is incomplete): %w", err)
	}
	return nil
}

func validateInviteGeneration(cfg InviteBookGenerationConfig) (inviteManifestFile, error) {
	manifest := inviteManifestFile{
		SchemaVersion:     inviteSchemaVersion,
		ProgramID:         cfg.ProgramID,
		MintKeyID:         cfg.MintKeyID,
		NotBefore:         cfg.NotBefore.UTC().Format(inviteTimeFormat),
		NotAfter:          cfg.NotAfter.UTC().Format(inviteTimeFormat),
		Seats:             cfg.Seats,
		VouchersPerInvite: cfg.VouchersPerInvite,
	}
	if strings.TrimSpace(cfg.OutputDir) == "" {
		return manifest, errors.New("mint: invite output directory is required")
	}
	if cfg.NotBefore.IsZero() || cfg.NotAfter.IsZero() {
		return manifest, errors.New("mint: invite not-before and not-after times are required")
	}
	if cfg.NotBefore.Nanosecond() != 0 || cfg.NotAfter.Nanosecond() != 0 {
		return manifest, errors.New("mint: invite times must use whole UTC seconds")
	}
	if err := validateInviteManifestFields(manifest, cfg.MintKeyID, false); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func parseInviteManifest(data []byte, expectedMintKeyID string) (inviteManifestFile, error) {
	var manifest inviteManifestFile
	if err := validateInviteManifestJSONShape(data); err != nil {
		return manifest, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("mint: parsing invite manifest: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return manifest, errors.New("mint: invite manifest contains more than one JSON value")
		}
		return manifest, fmt.Errorf("mint: parsing trailing invite manifest data: %w", err)
	}
	if err := validateInviteManifestFields(manifest, expectedMintKeyID, true); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func validateInviteManifestJSONShape(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("mint: invite manifest must be one JSON object")
	}
	allowed := map[string]struct{}{
		"schema_version": {}, "program_id": {}, "mint_key_id": {},
		"not_before": {}, "not_after": {}, "seats": {},
		"vouchers_per_invite": {}, "voucher_fingerprints": {},
	}
	seen := make(map[string]struct{}, len(allowed))
	for dec.More() {
		token, err := dec.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return errors.New("mint: invite manifest has an invalid field name")
		}
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("mint: invite manifest has unknown field %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("mint: invite manifest repeats field %q", name)
		}
		seen[name] = struct{}{}
		var discard json.RawMessage
		if err := dec.Decode(&discard); err != nil {
			return fmt.Errorf("mint: invite manifest field %q is malformed: %w", name, err)
		}
	}
	end, err := dec.Token()
	if err != nil || end != json.Delim('}') {
		return errors.New("mint: invite manifest object is incomplete")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("mint: invite manifest contains trailing data")
	}
	return nil
}

func validateInviteManifestFields(manifest inviteManifestFile, expectedMintKeyID string, requireFingerprints bool) error {
	if manifest.SchemaVersion != inviteSchemaVersion {
		return fmt.Errorf("mint: invite manifest schema_version is %d, want %d", manifest.SchemaVersion, inviteSchemaVersion)
	}
	if !inviteProgramPattern.MatchString(manifest.ProgramID) {
		return errors.New("mint: invite manifest program_id must be 1-64 ASCII letters, digits, dots, underscores, or hyphens, beginning with a letter or digit")
	}
	if manifest.MintKeyID == "" || manifest.MintKeyID != expectedMintKeyID {
		return fmt.Errorf("mint: invite manifest names mint key %q, but this mint is %q", manifest.MintKeyID, expectedMintKeyID)
	}
	if len(inviteMembershipRail(manifest.ProgramID, manifest.MintKeyID)) > 255 ||
		len(inviteClaimRail(manifest.ProgramID, manifest.MintKeyID)) > 255 {
		return errors.New("mint: invite program and mint key produce an oversized receipt namespace")
	}
	start, err := parseInviteTime(manifest.NotBefore)
	if err != nil {
		return fmt.Errorf("mint: invite manifest not_before: %w", err)
	}
	end, err := parseInviteTime(manifest.NotAfter)
	if err != nil {
		return fmt.Errorf("mint: invite manifest not_after: %w", err)
	}
	if !end.After(start) {
		return errors.New("mint: invite manifest not_after must be later than not_before")
	}
	if manifest.Seats < 1 || manifest.VouchersPerInvite < 1 {
		return errors.New("mint: invite manifest seats and vouchers_per_invite must both be positive")
	}
	if manifest.Seats > maxInviteVouchers || manifest.VouchersPerInvite > maxInviteVouchers ||
		manifest.Seats > maxInviteVouchers/manifest.VouchersPerInvite {
		return fmt.Errorf("mint: invite manifest represents more than %d vouchers", maxInviteVouchers)
	}
	want := manifest.Seats * manifest.VouchersPerInvite
	if !requireFingerprints {
		return nil
	}
	if len(manifest.Fingerprints) != want {
		return fmt.Errorf("mint: invite manifest has %d voucher fingerprints, want seats*vouchers_per_invite = %d", len(manifest.Fingerprints), want)
	}
	seen := make(map[[sha256.Size]byte]struct{}, len(manifest.Fingerprints))
	for i, encoded := range manifest.Fingerprints {
		if i > 0 && encoded <= manifest.Fingerprints[i-1] {
			return fmt.Errorf("mint: invite manifest voucher_fingerprints must be unique and sorted, so file order cannot encode invite groupings")
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil || len(raw) != sha256.Size || base64.RawURLEncoding.EncodeToString(raw) != encoded {
			return fmt.Errorf("mint: invite manifest voucher_fingerprints[%d] is not canonical base64url for a SHA-256 value", i)
		}
		var fingerprint [sha256.Size]byte
		copy(fingerprint[:], raw)
		if _, duplicate := seen[fingerprint]; duplicate {
			return fmt.Errorf("mint: invite manifest voucher_fingerprints[%d] is duplicated", i)
		}
		seen[fingerprint] = struct{}{}
	}
	return nil
}

func parseInviteTime(value string) (time.Time, error) {
	parsed, err := time.Parse(inviteTimeFormat, value)
	if err != nil {
		return time.Time{}, errors.New("must be a whole-second RFC 3339 UTC time ending in Z")
	}
	if parsed.Format(inviteTimeFormat) != value {
		return time.Time{}, errors.New("must use canonical UTC form")
	}
	return parsed.UTC(), nil
}

func mustParseInviteTime(value string) time.Time {
	parsed, err := parseInviteTime(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func inviteMembershipRail(programID, mintKeyID string) string {
	return "invite-membership-v1:" + programID + ":" + mintKeyID
}

func inviteClaimRail(programID, mintKeyID string) string {
	return "invite-claim-v1:" + programID + ":" + mintKeyID
}

func decodeInviteVoucher(encoded []byte) ([]byte, error) {
	if len(encoded) == 0 || strings.TrimSpace(string(encoded)) != string(encoded) {
		return nil, ErrInviteVoucherInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(string(encoded))
	if err != nil || len(raw) != inviteVoucherBytes || base64.RawURLEncoding.EncodeToString(raw) != string(encoded) {
		return nil, ErrInviteVoucherInvalid
	}
	return raw, nil
}

func deriveInviteVoucher(seed []byte, manifest inviteManifestFile, slot int) []byte {
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte("osanwe-invite-voucher-v1"))
	writeInviteField(mac, []byte(manifest.ProgramID))
	writeInviteField(mac, []byte(manifest.MintKeyID))
	writeInviteField(mac, []byte(manifest.NotBefore))
	writeInviteField(mac, []byte(manifest.NotAfter))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(slot))
	mac.Write(number[:])
	return mac.Sum(nil)
}

func writeInviteField(w io.Writer, value []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func readRegularFileBounded(path string, max int64) ([]byte, error) {
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 || !inviteFileOwnedByTrustedUser(parentInfo) {
		return nil, fmt.Errorf("manifest parent %s must be an owner-controlled, non-writable-by-others directory", parent)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 || !inviteFileOwnedByTrustedUser(info) {
		return nil, fmt.Errorf("%s has unsafe mode %04o; group and world must not be able to modify an authorization manifest", path, info.Mode().Perm())
	}
	if info.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, info.Size(), max)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%s did not remain a regular file while being opened", path)
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed identity while being opened", path)
	}
	if opened.Mode().Perm()&0o022 != 0 || !inviteFileOwnedByTrustedUser(opened) {
		return nil, fmt.Errorf("%s has unsafe mode %04o on its open descriptor", path, opened.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s grew past the %d-byte limit while being read", path, max)
	}
	return data, nil
}

func writeJSONExclusive(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileExclusive(path, data, mode)
}

func writeFileExclusive(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	wrote := false
	defer func() {
		if !wrote {
			_ = f.Close()
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	wrote = true
	return nil
}

func mustMarshalJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

var _ Authorizer = (*InviteAuthorizer)(nil)
