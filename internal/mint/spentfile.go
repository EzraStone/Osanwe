package mint

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// The file is a journal instead of a rewritten snapshot. A successful Spend
// is therefore one append and one fsync, with no rename window in which an old
// snapshot could become current again.
const spentFileHeader = "OSANWE-SPENT\x00\x01\n"

// A separate, stable inode serializes journal creation as well as mutation.
// Locking the journal itself is too late during first startup: another process
// can open the just-created empty journal and acquire its flock before the
// creator writes the header. The companion also pins the journal's device and
// inode so replacing the pathname with another valid journal fails closed.
const spentLockHeader = "OSANWE-SPENT-LOCK\x00\x01\n"

const (
	spentRecord  byte = 'S'
	refundRecord byte = 'F'
	retireRecord byte = 'K'

	spentFingerprintBytes = sha256.Size
	spentRecordFixedBytes = 1 + 2 + spentFingerprintBytes
	maxSpentRecordBytes   = spentRecordFixedBytes + MaxTokenBytes
	spentLockIdentitySize = len(spentLockHeader) + 8 + 8 + 4
)

type spentFileIdentity struct {
	device uint64
	inode  uint64
}

// FileSpentSet is a durable RedemptionStore backed by an append-only file.
//
// Appends are fsynced before an operation succeeds. An advisory lock on the
// stable <journal>.lock companion and a journal refresh make independent
// processes using the same file atomic with one another, including during
// first creation. The lock companion records the journal device and inode, so
// a replacement journal is rejected on restart; live stores also verify both
// pathnames around every mutation and fail closed if either has been replaced.
// Both files must live on a local filesystem: network filesystem lock and
// fsync guarantees differ, so cross-host gateways need a shared
// RedemptionStore implementation with an atomic create-if-absent primitive.
//
// The journal stores a SHA-256 fingerprint, not the nonce or signature that
// constituted the bearer token. Key IDs are retained so retired key epochs can
// be collected. The file is not an audit log and must never be copied aside
// and restored to an older version: doing so would revive tokens redeemed
// after the snapshot.
type FileSpentSet struct {
	mu sync.Mutex

	file     *os.File
	lock     *os.File
	path     string
	lockPath string
	identity spentFileIdentity
	offset   int64
	seen     map[[spentFingerprintBytes]byte]string

	// poison is sticky. Once the journal becomes ambiguous, continuing from an
	// in-memory view would turn an integrity failure into replay acceptance.
	poison error
}

// OpenFileSpentSet opens or creates a durable spent-token journal at path.
// Existing state is fully validated before the store is returned. Both files
// are created mode 0600 and an existing file accessible by group or world is
// refused: although records contain only opaque fingerprints, observing the
// journal grow would still expose redemption timing and volume.
func OpenFileSpentSet(path string) (*FileSpentSet, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("mint: spent-token database path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("mint: resolving spent-token database path %s: %w", path, err)
	}
	path = absPath

	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		lock, err = os.OpenFile(lockPath, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("mint: opening spent-token database lock %s: %w", lockPath, err)
	}
	if err := validateStorePath(lock, lockPath, "lock"); err != nil {
		_ = lock.Close()
		return nil, err
	}

	s := &FileSpentSet{
		lock:     lock,
		path:     path,
		lockPath: lockPath,
		seen:     make(map[[spentFingerprintBytes]byte]string),
	}
	closeOnError := func(err error) (*FileSpentSet, error) {
		if s.file != nil {
			_ = s.file.Close()
			s.file = nil
		}
		_ = lock.Close()
		s.lock = nil
		return nil, err
	}
	if err := s.lockFile(); err != nil {
		return closeOnError(err)
	}
	failLocked := func(err error) (*FileSpentSet, error) {
		s.unlockFile()
		return closeOnError(err)
	}

	// Open or create the journal only after taking the companion lock. This is
	// the ordering that makes simultaneous first startup deterministic.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		lockInfo, statErr := lock.Stat()
		if statErr != nil {
			return failLocked(fmt.Errorf("mint: inspecting spent-token database lock %s: %w", lockPath, statErr))
		}
		if lockInfo.Size() != 0 {
			return failLocked(fmt.Errorf("mint: spent-token database %s is missing but its lock identity remains; refusing to create an empty replacement", path))
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0o600)
		created = err == nil
	}
	if err != nil {
		return failLocked(fmt.Errorf("mint: opening spent-token database %s: %w", path, err))
	}
	s.file = f
	if err := validateStorePath(f, path, "journal"); err != nil {
		return failLocked(err)
	}
	info, err := f.Stat()
	if err != nil {
		return failLocked(fmt.Errorf("mint: inspecting spent-token database %s: %w", path, err))
	}
	if created {
		if err := s.appendBytes([]byte(spentFileHeader)); err != nil {
			return failLocked(err)
		}
	} else if info.Size() == 0 {
		return failLocked(fmt.Errorf("mint: spent-token database %s is empty; refusing to treat lost state as a new database", path))
	}
	if err := s.refreshLocked(); err != nil {
		return failLocked(err)
	}
	identity, err := identityOf(info)
	if err != nil {
		return failLocked(fmt.Errorf("mint: identifying spent-token database %s: %w", path, err))
	}
	// f.Stat above may precede header initialization, but neither operation can
	// change the inode. Pin only after the journal has been fully validated.
	s.identity = identity
	if err := s.pinOrVerifyIdentityLocked(); err != nil {
		return failLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return failLocked(err)
	}
	// Sync even for an existing file. If an earlier creator wrote the header
	// or lock identity but failed while syncing the directory entry, reopening
	// is the point at which that uncertainty must be resolved before accepting
	// traffic.
	if err := syncSpentParent(path); err != nil {
		return failLocked(err)
	}
	s.unlockFile()
	return s, nil
}

func syncSpentParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("mint: opening spent-token database directory for %s: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("mint: syncing spent-token database directory for %s: %w", path, err)
	}
	return nil
}

func validateStorePath(file *os.File, path, kind string) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("mint: inspecting spent-token database %s %s: %w", kind, path, err)
	}
	atPath, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("mint: spent-token database %s %s is missing or inaccessible: %w", kind, path, err)
	}
	if !opened.Mode().IsRegular() || !atPath.Mode().IsRegular() {
		return fmt.Errorf("mint: spent-token database %s %s is not a regular file (symlinks are not accepted)", kind, path)
	}
	if !os.SameFile(opened, atPath) {
		return fmt.Errorf("mint: spent-token database %s %s was replaced while open", kind, path)
	}
	if openedPerm := opened.Mode().Perm(); openedPerm&0o077 != 0 {
		return fmt.Errorf("mint: spent-token database %s %s has unsafe mode %04o on its open descriptor; group and world must have no access", kind, path, openedPerm)
	}
	if pathPerm := atPath.Mode().Perm(); pathPerm&0o077 != 0 {
		return fmt.Errorf("mint: spent-token database %s %s has unsafe mode %04o at its pathname; group and world must have no access", kind, path, pathPerm)
	}
	return nil
}

func identityOf(info os.FileInfo) (spentFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return spentFileIdentity{}, fmt.Errorf("unsupported file identity type %T", info.Sys())
	}
	return spentFileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func encodeSpentLockIdentity(identity spentFileIdentity) []byte {
	record := make([]byte, spentLockIdentitySize)
	copy(record, spentLockHeader)
	position := len(spentLockHeader)
	binary.BigEndian.PutUint64(record[position:position+8], identity.device)
	position += 8
	binary.BigEndian.PutUint64(record[position:position+8], identity.inode)
	position += 8
	binary.BigEndian.PutUint32(record[position:], crc32.ChecksumIEEE(record[:position]))
	return record
}

func decodeSpentLockIdentity(record []byte) (spentFileIdentity, error) {
	if len(record) != spentLockIdentitySize {
		return spentFileIdentity{}, fmt.Errorf("identity record is %d bytes, want %d", len(record), spentLockIdentitySize)
	}
	if string(record[:len(spentLockHeader)]) != spentLockHeader {
		return spentFileIdentity{}, errors.New("identity record has an unknown or corrupt header")
	}
	checksumAt := len(record) - 4
	if got, want := binary.BigEndian.Uint32(record[checksumAt:]), crc32.ChecksumIEEE(record[:checksumAt]); got != want {
		return spentFileIdentity{}, errors.New("identity record has a checksum mismatch")
	}
	position := len(spentLockHeader)
	return spentFileIdentity{
		device: binary.BigEndian.Uint64(record[position : position+8]),
		inode:  binary.BigEndian.Uint64(record[position+8 : position+16]),
	}, nil
}

func (s *FileSpentSet) readPinnedIdentityLocked() (spentFileIdentity, error) {
	info, err := s.lock.Stat()
	if err != nil {
		return spentFileIdentity{}, fmt.Errorf("mint: inspecting spent-token database lock %s: %w", s.lockPath, err)
	}
	if info.Size() != int64(spentLockIdentitySize) {
		return spentFileIdentity{}, fmt.Errorf("mint: spent-token database lock %s has an invalid identity record size %d", s.lockPath, info.Size())
	}
	record := make([]byte, spentLockIdentitySize)
	if _, err := s.lock.ReadAt(record, 0); err != nil {
		return spentFileIdentity{}, fmt.Errorf("mint: reading spent-token database lock identity %s: %w", s.lockPath, err)
	}
	identity, err := decodeSpentLockIdentity(record)
	if err != nil {
		return spentFileIdentity{}, fmt.Errorf("mint: spent-token database lock %s %w", s.lockPath, err)
	}
	return identity, nil
}

func (s *FileSpentSet) pinOrVerifyIdentityLocked() error {
	info, err := s.lock.Stat()
	if err != nil {
		return fmt.Errorf("mint: inspecting spent-token database lock %s: %w", s.lockPath, err)
	}
	if info.Size() == 0 {
		record := encodeSpentLockIdentity(s.identity)
		n, err := s.lock.WriteAt(record, 0)
		if err != nil {
			return fmt.Errorf("mint: pinning spent-token database identity in %s: %w", s.lockPath, err)
		}
		if n != len(record) {
			return fmt.Errorf("mint: pinning spent-token database identity in %s: %w", s.lockPath, io.ErrShortWrite)
		}
	} else {
		pinned, err := s.readPinnedIdentityLocked()
		if err != nil {
			return err
		}
		if pinned != s.identity {
			return fmt.Errorf("mint: spent-token database %s has device/inode %d/%d, but lock %s pins %d/%d; refusing a replacement journal",
				s.path, s.identity.device, s.identity.inode, s.lockPath, pinned.device, pinned.inode)
		}
	}
	if err := s.lock.Sync(); err != nil {
		return fmt.Errorf("mint: syncing spent-token database lock %s: %w", s.lockPath, err)
	}
	return nil
}

func (s *FileSpentSet) verifyPathsLocked() error {
	if err := validateStorePath(s.lock, s.lockPath, "lock"); err != nil {
		return err
	}
	if err := validateStorePath(s.file, s.path, "journal"); err != nil {
		return err
	}
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("mint: inspecting spent-token database %s: %w", s.path, err)
	}
	identity, err := identityOf(info)
	if err != nil {
		return fmt.Errorf("mint: identifying spent-token database %s: %w", s.path, err)
	}
	if identity != s.identity {
		return fmt.Errorf("mint: open spent-token database %s changed identity from %d/%d to %d/%d",
			s.path, s.identity.device, s.identity.inode, identity.device, identity.inode)
	}
	if info.Size() < s.offset {
		return fmt.Errorf("mint: spent-token database %s was truncated from %d to %d bytes", s.path, s.offset, info.Size())
	}
	pinned, err := s.readPinnedIdentityLocked()
	if err != nil {
		return err
	}
	if pinned != s.identity {
		return fmt.Errorf("mint: spent-token database lock %s no longer pins the open journal", s.lockPath)
	}
	return nil
}

// verifyCurrentStateLocked is used immediately after a refresh or append,
// when the open descriptor must have exactly the length represented by offset.
// A different length means a process that did not honor the companion lock
// changed the journal during the operation.
func (s *FileSpentSet) verifyCurrentStateLocked() error {
	if err := s.verifyPathsLocked(); err != nil {
		return err
	}
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("mint: inspecting spent-token database %s: %w", s.path, err)
	}
	if info.Size() != s.offset {
		return fmt.Errorf("mint: spent-token database %s changed concurrently from %d to %d bytes", s.path, s.offset, info.Size())
	}
	return nil
}

// Spend durably claims tok. It returns ErrAlreadySpent if another goroutine or
// process has already claimed it. Verification remains the caller's job.
func (s *FileSpentSet) Spend(tok *Token) error {
	keyID, fingerprint, err := spentIdentity(tok)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readyLocked(); err != nil {
		return err
	}
	if err := s.lockFile(); err != nil {
		return s.poisonLocked(err)
	}
	defer s.unlockFile()
	if err := s.verifyPathsLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.refreshLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if _, exists := s.seen[fingerprint]; exists {
		if err := s.verifyCurrentStateLocked(); err != nil {
			return s.poisonLocked(err)
		}
		return ErrAlreadySpent
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.appendRecordLocked(spentRecord, keyID, fingerprint); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return s.poisonLocked(err)
	}
	s.seen[fingerprint] = keyID
	return nil
}

// Refund durably releases tok. If persistence fails, the in-memory entry is
// deliberately retained and all later operations fail closed.
func (s *FileSpentSet) Refund(tok *Token) error {
	keyID, fingerprint, err := spentIdentity(tok)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readyLocked(); err != nil {
		return err
	}
	if err := s.lockFile(); err != nil {
		return s.poisonLocked(err)
	}
	defer s.unlockFile()
	if err := s.verifyPathsLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.refreshLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if _, exists := s.seen[fingerprint]; !exists {
		if err := s.verifyCurrentStateLocked(); err != nil {
			return s.poisonLocked(err)
		}
		return nil
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.appendRecordLocked(refundRecord, keyID, fingerprint); err != nil {
		return s.poisonLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return s.poisonLocked(err)
	}
	delete(s.seen, fingerprint)
	return nil
}

// Retire durably forgets redemptions made under keyID. Removing the matching
// verification key remains what invalidates unspent tokens from that epoch.
func (s *FileSpentSet) Retire(keyID string) (int, error) {
	if keyID == "" || len(keyID) > MaxTokenBytes {
		return 0, errors.New("mint: invalid key id for spent-token retirement")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readyLocked(); err != nil {
		return 0, err
	}
	if err := s.lockFile(); err != nil {
		return 0, s.poisonLocked(err)
	}
	defer s.unlockFile()
	if err := s.verifyPathsLocked(); err != nil {
		return 0, s.poisonLocked(err)
	}
	if err := s.refreshLocked(); err != nil {
		return 0, s.poisonLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return 0, s.poisonLocked(err)
	}

	n := 0
	for _, id := range s.seen {
		if id == keyID {
			n++
		}
	}
	if n == 0 {
		if err := s.verifyCurrentStateLocked(); err != nil {
			return 0, s.poisonLocked(err)
		}
		return 0, nil
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return 0, s.poisonLocked(err)
	}
	if err := s.appendRecordLocked(retireRecord, keyID, [spentFingerprintBytes]byte{}); err != nil {
		return 0, s.poisonLocked(err)
	}
	if err := s.verifyCurrentStateLocked(); err != nil {
		return 0, s.poisonLocked(err)
	}
	for fingerprint, id := range s.seen {
		if id == keyID {
			delete(s.seen, fingerprint)
		}
	}
	return n, nil
}

// Len reports the number of redemptions visible to this process. It is for
// diagnostics only; Spend performs a locked refresh before every decision.
func (s *FileSpentSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// Close releases the journal and companion lock files. Every successful
// mutation was already fsynced, so Close does not create a new durability
// boundary.
func (s *FileSpentSet) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var closeErrors []error
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("mint: closing spent-token database %s: %w", s.path, err))
		}
		s.file = nil
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("mint: closing spent-token database lock %s: %w", s.lockPath, err))
		}
		s.lock = nil
	}
	return errors.Join(closeErrors...)
}

func spentIdentity(tok *Token) (string, [spentFingerprintBytes]byte, error) {
	if tok == nil {
		return "", [spentFingerprintBytes]byte{}, errors.New("mint: nil token")
	}
	if tok.KeyID == "" || len(tok.KeyID) > MaxTokenBytes || len(tok.Nonce) == 0 || len(tok.Nonce) > MaxTokenBytes {
		return "", [spentFingerprintBytes]byte{}, errors.New("mint: invalid token identity")
	}

	// Length-prefix the key ID so no two (key, nonce) pairs have the same byte
	// representation. Parsed tokens are far below this bound.
	buf := make([]byte, 4+len(tok.KeyID)+len(tok.Nonce))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(tok.KeyID)))
	copy(buf[4:], tok.KeyID)
	copy(buf[4+len(tok.KeyID):], tok.Nonce)
	return tok.KeyID, sha256.Sum256(buf), nil
}

func (s *FileSpentSet) readyLocked() error {
	if s.poison != nil {
		return fmt.Errorf("mint: spent-token database %s is unavailable after an integrity failure: %w", s.path, s.poison)
	}
	if s.file == nil || s.lock == nil {
		return fmt.Errorf("mint: spent-token database %s is closed", s.path)
	}
	return nil
}

func (s *FileSpentSet) poisonLocked(err error) error {
	if s.poison == nil {
		s.poison = err
	}
	return fmt.Errorf("mint: spent-token database %s failed closed: %w", s.path, err)
}

func (s *FileSpentSet) lockFile() error {
	if s.lock == nil {
		return fmt.Errorf("mint: spent-token database lock %s is closed", s.lockPath)
	}
	// Validate before waiting so an already-replaced lock is rejected promptly,
	// and again after acquiring it to close the replacement-during-wait window.
	if err := validateStorePath(s.lock, s.lockPath, "lock"); err != nil {
		return err
	}
	for {
		err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("mint: locking spent-token database companion %s: %w", s.lockPath, err)
		}
		break
	}
	if err := validateStorePath(s.lock, s.lockPath, "lock"); err != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		return err
	}
	return nil
}

func (s *FileSpentSet) unlockFile() {
	if s.lock == nil {
		return
	}
	// A failed unlock leaves this descriptor holding the lock; it does not make
	// a committed redemption non-durable. Closing the process or store releases
	// it, and no unsafe result should replace a successful fsync.
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
}

func (s *FileSpentSet) refreshLocked() error {
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("mint: inspecting spent-token database %s: %w", s.path, err)
	}
	if info.Size() < s.offset {
		return fmt.Errorf("mint: spent-token database %s was truncated from %d to %d bytes", s.path, s.offset, info.Size())
	}
	if info.Size() == s.offset {
		return nil
	}

	reader := io.NewSectionReader(s.file, s.offset, info.Size()-s.offset)
	position := s.offset
	if position == 0 {
		header := make([]byte, len(spentFileHeader))
		if _, err := io.ReadFull(reader, header); err != nil {
			return fmt.Errorf("mint: spent-token database %s has a truncated header", s.path)
		}
		if string(header) != spentFileHeader {
			return fmt.Errorf("mint: spent-token database %s has an unknown or corrupt header", s.path)
		}
		position += int64(len(header))
	}

	for position < info.Size() {
		var sizeBytes [4]byte
		if _, err := io.ReadFull(reader, sizeBytes[:]); err != nil {
			return fmt.Errorf("mint: spent-token database %s has a truncated record length at byte %d", s.path, position)
		}
		payloadSize := int(binary.BigEndian.Uint32(sizeBytes[:]))
		if payloadSize < spentRecordFixedBytes || payloadSize > maxSpentRecordBytes {
			return fmt.Errorf("mint: spent-token database %s has invalid record size %d at byte %d", s.path, payloadSize, position)
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return fmt.Errorf("mint: spent-token database %s has a truncated record at byte %d", s.path, position)
		}
		var checksumBytes [4]byte
		if _, err := io.ReadFull(reader, checksumBytes[:]); err != nil {
			return fmt.Errorf("mint: spent-token database %s has a truncated checksum at byte %d", s.path, position)
		}
		if got, want := binary.BigEndian.Uint32(checksumBytes[:]), crc32.ChecksumIEEE(payload); got != want {
			return fmt.Errorf("mint: spent-token database %s has a checksum mismatch at byte %d", s.path, position)
		}
		if err := s.applyRecord(payload, position); err != nil {
			return err
		}
		position += int64(4 + payloadSize + 4)
	}
	s.offset = position
	return nil
}

func (s *FileSpentSet) applyRecord(payload []byte, position int64) error {
	keyLen := int(binary.BigEndian.Uint16(payload[1:3]))
	if keyLen == 0 || keyLen > MaxTokenBytes || len(payload) != spentRecordFixedBytes+keyLen {
		return fmt.Errorf("mint: spent-token database %s has an invalid key id length at byte %d", s.path, position)
	}
	keyID := string(payload[3 : 3+keyLen])
	var fingerprint [spentFingerprintBytes]byte
	copy(fingerprint[:], payload[3+keyLen:])

	switch payload[0] {
	case spentRecord:
		s.seen[fingerprint] = keyID
	case refundRecord:
		delete(s.seen, fingerprint)
	case retireRecord:
		if fingerprint != ([spentFingerprintBytes]byte{}) {
			return fmt.Errorf("mint: spent-token database %s has an invalid retirement record at byte %d", s.path, position)
		}
		for existing, id := range s.seen {
			if id == keyID {
				delete(s.seen, existing)
			}
		}
	default:
		return fmt.Errorf("mint: spent-token database %s has unknown operation %q at byte %d", s.path, payload[0], position)
	}
	return nil
}

func (s *FileSpentSet) appendRecordLocked(operation byte, keyID string, fingerprint [spentFingerprintBytes]byte) error {
	payload := make([]byte, spentRecordFixedBytes+len(keyID))
	payload[0] = operation
	binary.BigEndian.PutUint16(payload[1:3], uint16(len(keyID)))
	copy(payload[3:], keyID)
	copy(payload[3+len(keyID):], fingerprint[:])

	record := make([]byte, 4+len(payload)+4)
	binary.BigEndian.PutUint32(record[:4], uint32(len(payload)))
	copy(record[4:], payload)
	binary.BigEndian.PutUint32(record[4+len(payload):], crc32.ChecksumIEEE(payload))
	if err := s.appendBytes(record); err != nil {
		return err
	}
	s.offset += int64(len(record))
	return nil
}

func (s *FileSpentSet) appendBytes(data []byte) error {
	written := 0
	for written < len(data) {
		n, err := s.file.Write(data[written:])
		written += n
		if err != nil {
			return fmt.Errorf("mint: appending spent-token database %s: %w", s.path, err)
		}
		if n == 0 {
			return fmt.Errorf("mint: appending spent-token database %s: %w", s.path, io.ErrShortWrite)
		}
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("mint: syncing spent-token database %s: %w", s.path, err)
	}
	return nil
}

var _ RedemptionStore = (*SpentSet)(nil)
var _ RedemptionStore = (*FileSpentSet)(nil)
