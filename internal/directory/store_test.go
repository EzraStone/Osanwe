package directory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func acceptList(t *testing.T, fingerprints ...string) (*AcceptList, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accept")
	body := "# operators admitted to this authority\n"
	for _, fp := range fingerprints {
		body += fp + " some-operator\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a, err := NewAcceptList(path)
	if err != nil {
		t.Fatalf("NewAcceptList: %v", err)
	}
	return a, path
}

func signedDescriptor(t *testing.T, id *Identity, nickname string, published time.Time) (*Descriptor, []byte) {
	t.Helper()
	d := &Descriptor{
		Nickname:     nickname,
		Address:      nickname + ".example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Published:    published,
		Expires:      published.Add(24 * time.Hour),
	}
	encoded, err := d.Sign(id)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return d, encoded
}

func TestStoreRejectsIdentitiesNotOnTheAcceptList(t *testing.T) {
	admitted, _ := GenerateIdentity()
	stranger, _ := GenerateIdentity()
	list, _ := acceptList(t, admitted.Fingerprint())

	s := &Store{Dir: t.TempDir(), Accept: list}
	now := time.Now()

	// An open endpoint would let anyone register relays, and a directory
	// listing a thousand attacker-run relays has handed over almost every
	// client without breaking a signature.
	d, encoded := signedDescriptor(t, stranger, "attacker", now)
	err := s.Put(d, encoded, now)
	var notAccepted *ErrNotAccepted
	if !errors.As(err, &notAccepted) {
		t.Fatalf("err = %v, want ErrNotAccepted", err)
	}
	if _, statErr := os.Stat(s.FileFor(d.Identity)); statErr == nil {
		t.Error("a rejected submission was written to disk anyway")
	}

	// The admitted operator works.
	ok, okEncoded := signedDescriptor(t, admitted, "friend", now)
	if err := s.Put(ok, okEncoded, now); err != nil {
		t.Fatalf("admitted identity was rejected: %v", err)
	}
}

func TestStoreNilAcceptListDeniesEverything(t *testing.T) {
	id, _ := GenerateIdentity()
	s := &Store{Dir: t.TempDir()} // no accept list configured
	now := time.Now()
	d, encoded := signedDescriptor(t, id, "alpha", now)

	if err := s.Put(d, encoded, now); err == nil {
		t.Fatal("a Store with no accept list accepted a submission; it must fail closed")
	}
}

func TestStoreRefusesRollback(t *testing.T) {
	id, _ := GenerateIdentity()
	list, _ := acceptList(t, id.Fingerprint())
	s := &Store{Dir: t.TempDir(), Accept: list}

	now := time.Now()
	older, olderEnc := signedDescriptor(t, id, "alpha", now.Add(-2*time.Hour))
	newer, newerEnc := signedDescriptor(t, id, "alpha", now)

	if err := s.Put(newer, newerEnc, now); err != nil {
		t.Fatalf("first Put: %v", err)
	}

	// The old descriptor's signature is still perfectly valid, which is
	// exactly why freshness has to be checked separately. Replaying it would
	// move the relay back to a previous address or key.
	err := s.Put(older, olderEnc, now)
	var stale *ErrStale
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}

	// The newer descriptor must still be the one on disk.
	stored, readErr := os.ReadFile(s.FileFor(id.Fingerprint()))
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	got, parseErr := ParseDescriptor(stored)
	if parseErr != nil {
		t.Fatalf("ParseDescriptor: %v", parseErr)
	}
	if !got.Published.Equal(newer.Published.UTC().Truncate(time.Second)) {
		t.Error("a replayed older descriptor overwrote the newer one")
	}
}

func TestStoreRefusesResubmittingTheSameDescriptor(t *testing.T) {
	id, _ := GenerateIdentity()
	list, _ := acceptList(t, id.Fingerprint())
	s := &Store{Dir: t.TempDir(), Accept: list}

	now := time.Now()
	d, encoded := signedDescriptor(t, id, "alpha", now)

	if err := s.Put(d, encoded, now); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	// Equal timestamps must be refused too, or a captured document could be
	// replayed indefinitely.
	if err := s.Put(d, encoded, now); err == nil {
		t.Fatal("re-submitting an identical descriptor was accepted")
	}
}

func TestStoreAcceptsAGenuineUpdate(t *testing.T) {
	id, _ := GenerateIdentity()
	list, _ := acceptList(t, id.Fingerprint())
	s := &Store{Dir: t.TempDir(), Accept: list}

	now := time.Now()
	first, firstEnc := signedDescriptor(t, id, "alpha", now.Add(-time.Hour))
	if err := s.Put(first, firstEnc, now); err != nil {
		t.Fatalf("first Put: %v", err)
	}

	second, secondEnc := signedDescriptor(t, id, "alpha-renamed", now)
	if err := s.Put(second, secondEnc, now); err != nil {
		t.Fatalf("update rejected: %v", err)
	}

	stored, _ := os.ReadFile(s.FileFor(id.Fingerprint()))
	got, err := ParseDescriptor(stored)
	if err != nil {
		t.Fatalf("ParseDescriptor: %v", err)
	}
	if got.Nickname != "alpha-renamed" {
		t.Errorf("nickname = %q, want the updated value", got.Nickname)
	}
}

func TestStoreRejectsExpiredSubmissions(t *testing.T) {
	id, _ := GenerateIdentity()
	list, _ := acceptList(t, id.Fingerprint())
	s := &Store{Dir: t.TempDir(), Accept: list}

	now := time.Now()
	d, encoded := signedDescriptor(t, id, "alpha", now.Add(-48*time.Hour)) // expired 24h ago
	if err := s.Put(d, encoded, now); err == nil {
		t.Fatal("an expired descriptor was accepted")
	}
}

func TestOneRelayCannotOverwriteAnother(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	list, _ := acceptList(t, a.Fingerprint(), b.Fingerprint())
	s := &Store{Dir: t.TempDir(), Accept: list}

	now := time.Now()
	// Both claim the same nickname. Storage is keyed by identity, so they must
	// not collide.
	da, ea := signedDescriptor(t, a, "samename", now)
	db, eb := signedDescriptor(t, b, "samename", now)

	if err := s.Put(da, ea, now); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := s.Put(db, eb, now); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	if s.FileFor(a.Fingerprint()) == s.FileFor(b.Fingerprint()) {
		t.Fatal("two identities map to the same file; one relay could overwrite another")
	}
	for _, id := range []*Identity{a, b} {
		data, err := os.ReadFile(s.FileFor(id.Fingerprint()))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		got, err := ParseDescriptor(data)
		if err != nil {
			t.Fatalf("ParseDescriptor: %v", err)
		}
		if got.Identity != id.Fingerprint() {
			t.Errorf("file for %s holds a descriptor from %s", id.Fingerprint(), got.Identity)
		}
	}
}

func TestFileForStaysInsideTheDirectory(t *testing.T) {
	s := &Store{Dir: "/var/lib/osanwe/descriptors"}
	// A fingerprint carries base64 padding and slashes; the name must be
	// hashed so nothing can escape the directory.
	for _, identity := range []string{
		"ed25519:AAAA/BBBB+CCCC=",
		"../../etc/passwd",
		"ed25519:" + strings.Repeat("/", 40),
	} {
		got := s.FileFor(identity)
		if filepath.Dir(got) != s.Dir {
			t.Errorf("FileFor(%q) = %q, which is outside %q", identity, got, s.Dir)
		}
		if strings.Contains(filepath.Base(got), "/") || strings.Contains(filepath.Base(got), "..") {
			t.Errorf("FileFor(%q) produced an unsafe name %q", identity, filepath.Base(got))
		}
	}
}

func TestAcceptListReloadsWithoutRestart(t *testing.T) {
	admitted, _ := GenerateIdentity()
	newcomer, _ := GenerateIdentity()
	list, path := acceptList(t, admitted.Fingerprint())

	if list.Allows(newcomer.Fingerprint()) {
		t.Fatal("an identity not in the file was allowed")
	}

	// An operator admitting a relay should not have to restart the authority.
	body, _ := os.ReadFile(path)
	body = append(body, []byte(newcomer.Fingerprint()+" newcomer\n")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := list.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !list.Allows(newcomer.Fingerprint()) {
		t.Error("Reload did not pick up a newly admitted identity")
	}
	if list.Len() != 2 {
		t.Errorf("Len = %d, want 2", list.Len())
	}
}

func TestAcceptListRejectsAMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accept")
	if err := os.WriteFile(path, []byte("not-a-key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewAcceptList(path); err == nil {
		t.Fatal("a malformed accept list was loaded; silently ignoring a typo could admit nobody or the wrong party")
	}
}

func TestAcceptListIgnoresCommentsAndBlanks(t *testing.T) {
	id, _ := GenerateIdentity()
	path := filepath.Join(t.TempDir(), "accept")
	body := "# a comment\n\n   \n" + id.Fingerprint() + "   north relay, contact ops@example\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	list, err := NewAcceptList(path)
	if err != nil {
		t.Fatalf("NewAcceptList: %v", err)
	}
	if !list.Allows(id.Fingerprint()) {
		t.Error("a valid entry with a trailing label was not admitted")
	}
	if list.Len() != 1 {
		t.Errorf("Len = %d, want 1", list.Len())
	}
}

func TestEncodedRoundTrips(t *testing.T) {
	id, _ := GenerateIdentity()
	_, encoded := signedDescriptor(t, id, "alpha", time.Now())

	parsed, err := ParseDescriptor(encoded)
	if err != nil {
		t.Fatalf("ParseDescriptor: %v", err)
	}
	again := parsed.Encoded()
	if string(again) != string(encoded) {
		t.Error("Encoded did not reproduce the exact signed document")
	}
	// And the reproduction must itself verify.
	if _, err := ParseDescriptor(again); err != nil {
		t.Errorf("re-encoded descriptor no longer verifies: %v", err)
	}
}
