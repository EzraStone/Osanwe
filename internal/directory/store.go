package directory

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AcceptList is the set of relay identities an authority will publish.
//
// Submission is default-deny, which is the whole point. An open endpoint lets
// anyone register relays, and a directory that lists a thousand attacker-run
// relays alongside three honest ones has handed the attacker almost every
// client, without breaking a single signature. Deciding which operators to
// carry is a human judgement, and this is where an authority records it.
type AcceptList struct {
	path string

	mu      sync.RWMutex
	allowed map[string]string // fingerprint -> label
	loaded  time.Time
}

// NewAcceptList reads an accept list from a file of "fingerprint [label]"
// lines. Blank lines and those starting with # are ignored.
func NewAcceptList(path string) (*AcceptList, error) {
	a := &AcceptList{path: path}
	if err := a.Reload(); err != nil {
		return nil, err
	}
	return a, nil
}

// Reload re-reads the file, so an operator can admit a new relay without
// restarting the authority.
func (a *AcceptList) Reload() error {
	f, err := os.Open(a.path)
	if err != nil {
		return fmt.Errorf("directory: opening accept list: %w", err)
	}
	defer f.Close()

	allowed := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fp, label, _ := strings.Cut(text, " ")
		pub, err := DecodeKey(fp)
		if err != nil {
			return fmt.Errorf("directory: accept list line %d: %w", line, err)
		}
		allowed[EncodeKey(pub)] = strings.TrimSpace(label)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("directory: reading accept list: %w", err)
	}

	a.mu.Lock()
	a.allowed, a.loaded = allowed, time.Now()
	a.mu.Unlock()
	return nil
}

// Allows reports whether an identity may publish.
func (a *AcceptList) Allows(identity string) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.allowed[strings.TrimSpace(identity)]
	return ok
}

// Len reports how many identities are admitted.
func (a *AcceptList) Len() int {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.allowed)
}

// Store persists submitted descriptors on disk.
type Store struct {
	Dir    string
	Accept *AcceptList

	mu sync.Mutex
}

// FileFor returns the path a given identity's descriptor is stored at.
//
// The name is derived from the identity, not from the nickname, so one relay
// can never overwrite another's entry by choosing a colliding nickname. It is
// hashed so that a fingerprint containing base64 padding or slashes cannot
// escape the directory.
func (s *Store) FileFor(identity string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(identity)))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:16])+".desc")
}

// ErrNotAccepted is returned when an identity is not on the accept list.
type ErrNotAccepted struct{ Identity string }

func (e *ErrNotAccepted) Error() string {
	return fmt.Sprintf("identity %s is not on this authority's accept list; "+
		"its operator must add the fingerprint before the relay can be published", e.Identity)
}

// ErrStale is returned when a submission is older than what is already stored.
type ErrStale struct {
	Stored    time.Time
	Submitted time.Time
}

func (e *ErrStale) Error() string {
	return fmt.Sprintf("submission was published %s but the stored descriptor is newer (%s); "+
		"refusing to roll a relay back",
		e.Submitted.UTC().Format(time.RFC3339), e.Stored.UTC().Format(time.RFC3339))
}

// Put validates and stores a submitted descriptor.
//
// The caller is expected to have parsed it already, which is what verifies the
// signature. Put adds the checks that depend on state the parser cannot see.
func (s *Store) Put(d *Descriptor, encoded []byte, now time.Time) error {
	if !s.Accept.Allows(d.Identity) {
		return &ErrNotAccepted{Identity: d.Identity}
	}
	if d.Expired(now) {
		return fmt.Errorf("descriptor is outside its validity window (published %s, expires %s)",
			d.Published.UTC().Format(time.RFC3339), d.Expires.UTC().Format(time.RFC3339))
	}

	path := s.FileFor(d.Identity)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Rollback protection. Without it, anyone who kept a copy of an older
	// descriptor could replay it and move a relay back to a previous address
	// or key. The signature on that old document is still perfectly valid,
	// which is exactly why freshness has to be checked separately.
	if existing, err := os.ReadFile(path); err == nil {
		prev, err := ParseDescriptor(existing)
		if err == nil {
			// Compare at the precision the wire format carries. A descriptor
			// straight from Sign still has nanoseconds, while one that has
			// been through Parse is truncated to seconds, and comparing them
			// raw would make replay protection depend on how the caller
			// happened to obtain the descriptor.
			submitted := d.Published.Truncate(time.Second)
			stored := prev.Published.Truncate(time.Second)
			if !submitted.After(stored) {
				return &ErrStale{Stored: stored, Submitted: submitted}
			}
			// Belt and braces: the filename is derived from the identity, so
			// this should be impossible. If it ever fires, something is wrong
			// with the naming and overwriting would be the wrong response.
			if prev.Identity != d.Identity {
				return fmt.Errorf("stored descriptor at %s belongs to %s, not %s", path, prev.Identity, d.Identity)
			}
		}
	}

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("creating descriptor directory: %w", err)
	}

	// Write to a temporary file and rename, so a reader mid-rebuild never sees
	// a half-written descriptor.
	tmp, err := os.CreateTemp(s.Dir, ".submit-*")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("writing descriptor: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing descriptor: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("setting descriptor mode: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installing descriptor: %w", err)
	}
	return nil
}

// Encoded re-renders a parsed descriptor as the exact signed document it came
// from, so a store or a relay can pass it on without disturbing the signature.
func (d *Descriptor) Encoded() []byte {
	if d.raw == nil || d.signature == "" {
		return nil
	}
	out := append([]byte(nil), d.raw...)
	return append(out, []byte(signatureKey+" "+d.signature+"\n")...)
}
