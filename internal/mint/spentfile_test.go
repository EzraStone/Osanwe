package mint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	spentOpenHelperEnv  = "OSANWE_SPENT_OPEN_HELPER"
	spentOpenHelperPath = "OSANWE_SPENT_OPEN_PATH"
)

func spentToken(n int) *Token {
	return &Token{
		KeyID: "mint-test-epoch",
		Nonce: []byte(fmt.Sprintf("opaque-redemption-%08d", n)),
		Sig:   []byte("signature-is-not-part-of-the-redemption-key"),
	}
}

func openSpentFile(t *testing.T, path string) *FileSpentSet {
	t.Helper()
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("OpenFileSpentSet: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestFileSpentSetConcurrentProcessInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")

	const processes = 8
	type childProcess struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	gateReader, gateWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create process gate: %v", err)
	}
	children := make([]*childProcess, 0, processes)
	for i := 0; i < processes; i++ {
		child := &childProcess{}
		child.command = exec.Command(os.Args[0], "-test.run=^TestFileSpentSetConcurrentProcessOpenHelper$")
		child.command.Env = append(os.Environ(),
			spentOpenHelperEnv+"=1",
			spentOpenHelperPath+"="+path,
		)
		child.command.Stdout = &child.output
		child.command.Stderr = &child.output
		child.command.Stdin = gateReader
		if err := child.command.Start(); err != nil {
			_ = gateReader.Close()
			_ = gateWriter.Close()
			for _, started := range children {
				_ = started.command.Wait()
			}
			t.Fatalf("start child %d: %v", i, err)
		}
		children = append(children, child)
	}

	// All helpers wait for EOF before opening the same initially absent path.
	if err := gateReader.Close(); err != nil {
		t.Errorf("close parent gate reader: %v", err)
	}
	if err := gateWriter.Close(); err != nil {
		t.Errorf("release child processes: %v", err)
	}
	for i := range children {
		if err := children[i].command.Wait(); err != nil {
			t.Errorf("child %d: %v\n%s", i, err, children[i].output.String())
		}
	}
	if t.Failed() {
		return
	}

	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open after concurrent initialization: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close after concurrent initialization: %v", err)
	}
	for _, companion := range []string{path, path + ".lock"} {
		info, err := os.Stat(companion)
		if err != nil {
			t.Fatalf("Stat %s: %v", companion, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %04o, want no group/world access", companion, info.Mode().Perm())
		}
	}
}

func TestFileSpentSetConcurrentProcessOpenHelper(t *testing.T) {
	if os.Getenv(spentOpenHelperEnv) != "1" {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatalf("wait for parent gate: %v", err)
	}
	store, err := OpenFileSpentSet(os.Getenv(spentOpenHelperPath))
	if err != nil {
		t.Fatalf("concurrent OpenFileSpentSet: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("concurrent Close: %v", err)
	}
}

func TestFileSpentSetCloseReleasesJournalAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	journal := store.file
	companion := store.lock
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if store.file != nil || store.lock != nil {
		t.Fatalf("Close retained descriptors: journal=%v lock=%v", store.file, store.lock)
	}
	if _, err := journal.Stat(); err == nil {
		t.Error("journal descriptor remained usable after Close")
	}
	if _, err := companion.Stat(); err == nil {
		t.Error("companion lock descriptor remained usable after Close")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestFileSpentSetSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	tok := spentToken(1)

	first, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Spend(tok); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	second, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := second.Spend(tok); !errors.Is(err, ErrAlreadySpent) {
		t.Fatalf("spend after restart = %v, want ErrAlreadySpent", err)
	}
	if got := second.Len(); got != 1 {
		t.Fatalf("Len after restart = %d, want 1", got)
	}
	if err := second.Refund(tok); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	third, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("reopen after refund: %v", err)
	}
	defer third.Close()
	if err := third.Spend(tok); err != nil {
		t.Fatalf("spend after durable refund: %v", err)
	}
}

func TestFileSpentSetIsAtomicAcrossIndependentHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	left := openSpentFile(t, path)
	right := openSpentFile(t, path)
	tok := spentToken(2)

	const racers = 64
	start := make(chan struct{})
	results := make([]error, racers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				results[i] = left.Spend(tok)
			} else {
				results[i] = right.Spend(tok)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for i, err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrAlreadySpent):
		default:
			t.Fatalf("racer %d returned unexpected error: %v", i, err)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d of %d concurrent spends succeeded, want exactly 1", accepted, racers)
	}
}

func TestFileSpentSetHandlesSeeEachOthersRefunds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	left := openSpentFile(t, path)
	right := openSpentFile(t, path)
	tok := spentToken(3)

	if err := left.Spend(tok); err != nil {
		t.Fatalf("left Spend: %v", err)
	}
	if err := right.Refund(tok); err != nil {
		t.Fatalf("right Refund: %v", err)
	}
	if err := left.Spend(tok); err != nil {
		t.Fatalf("left Spend after cross-handle refund: %v", err)
	}
}

func TestFileSpentSetRetirementSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first := spentToken(4)
	second := spentToken(5)
	second.KeyID = "mint-other-epoch"
	if err := store.Spend(first); err != nil {
		t.Fatalf("Spend first: %v", err)
	}
	if err := store.Spend(second); err != nil {
		t.Fatalf("Spend second: %v", err)
	}
	if n, err := store.Retire(first.KeyID); err != nil || n != 1 {
		t.Fatalf("Retire = (%d, %v), want (1, nil)", n, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Spend(first); err != nil {
		t.Fatalf("retired epoch was not removed durably: %v", err)
	}
	if err := reopened.Spend(second); !errors.Is(err, ErrAlreadySpent) {
		t.Fatalf("other epoch spend = %v, want ErrAlreadySpent", err)
	}
}

func TestFileSpentSetRefusesCorruptJournal(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "bad header",
			mutate: func(data []byte) []byte {
				data[0] ^= 0xff
				return data
			},
		},
		{
			name: "bad checksum",
			mutate: func(data []byte) []byte {
				data[len(data)-1] ^= 0xff
				return data
			},
		},
		{
			name: "truncated record",
			mutate: func(data []byte) []byte {
				return data[:len(data)-1]
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spent.db")
			store, err := OpenFileSpentSet(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := store.Spend(spentToken(6)); err != nil {
				t.Fatalf("Spend: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if err := os.WriteFile(path, tt.mutate(data), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if reopened, err := OpenFileSpentSet(path); err == nil {
				reopened.Close()
				t.Fatal("opened a corrupt redemption journal")
			}
		})
	}
}

func TestFileSpentSetPoisonsItselfAfterAmbiguousState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store := openSpentFile(t, path)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open corrupter: %v", err)
	}
	if _, err := f.Write([]byte("partial")); err != nil {
		t.Fatalf("append corruption: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync corruption: %v", err)
	}
	f.Close()

	if err := store.Spend(spentToken(7)); err == nil || !strings.Contains(err.Error(), "failed closed") {
		t.Fatalf("Spend with corrupt tail = %v, want fail-closed error", err)
	}
	if err := os.Truncate(path, int64(len(spentFileHeader))); err != nil {
		t.Fatalf("repair journal: %v", err)
	}
	if err := store.Spend(spentToken(8)); err == nil || !strings.Contains(err.Error(), "integrity failure") {
		t.Fatalf("Spend after external repair = %v, want sticky integrity error", err)
	}
}

func TestFileSpentSetFailedRefundKeepsTokenClaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tok := spentToken(9)
	if err := store.Spend(tok); err != nil {
		t.Fatalf("Spend: %v", err)
	}

	// Closing the descriptor underneath the store simulates an unrecoverable
	// persistence failure at the operation boundary.
	if err := store.file.Close(); err != nil {
		t.Fatalf("close underlying file: %v", err)
	}
	if err := store.Refund(tok); err == nil {
		t.Fatal("Refund succeeded after the durable store failed")
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("Len after failed Refund = %d, want token retained", got)
	}
	store.file = nil
	if err := store.Close(); err != nil {
		t.Fatalf("close companion lock: %v", err)
	}
}

func TestFileSpentSetFailedSpendNeverReturnsSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.file.Close(); err != nil {
		t.Fatalf("close underlying file: %v", err)
	}
	if err := store.Spend(spentToken(10)); err == nil {
		t.Fatal("Spend succeeded after the durable store failed")
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("Len after failed Spend = %d, want 0", got)
	}
	if err := store.Spend(spentToken(11)); err == nil || !strings.Contains(err.Error(), "integrity failure") {
		t.Fatalf("second Spend = %v, want sticky integrity error", err)
	}
	store.file = nil
	if err := store.Close(); err != nil {
		t.Fatalf("close companion lock: %v", err)
	}
}

func TestFileSpentSetDoesNotPersistBearerTokenMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store := openSpentFile(t, path)
	tok := &Token{
		KeyID: "mint-public-epoch",
		Nonce: []byte("nonce-that-must-not-be-written-verbatim"),
		Sig:   []byte("signature-that-must-not-be-written-verbatim"),
	}
	if err := store.Spend(tok); err != nil {
		t.Fatalf("Spend: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.Contains(data, tok.Nonce) || bytes.Contains(data, tok.Sig) {
		t.Fatal("redemption journal contains bearer token material verbatim")
	}
}

func TestFileSpentSetRefusesUnsafePermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spent.db")
			if err := os.WriteFile(path, []byte(spentFileHeader), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("Chmod: %v", err)
			}
			if store, err := OpenFileSpentSet(path); err == nil {
				store.Close()
				t.Fatalf("opened a redemption journal with mode %04o", mode)
			}
		})
	}
}

func TestFileSpentSetRefusesUnsafeLockPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Chmod(path+".lock", 0o640); err != nil {
		t.Fatalf("Chmod lock: %v", err)
	}
	if reopened, err := OpenFileSpentSet(path); err == nil {
		_ = reopened.Close()
		t.Fatal("opened a redemption journal with a group-readable companion lock")
	} else if !strings.Contains(err.Error(), "unsafe mode") {
		t.Fatalf("open error = %v, want unsafe-mode rejection", err)
	}
}

func TestFileSpentSetRefusesCorruptLockIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "checksum",
			mutate: func(data []byte) []byte {
				data[len(data)-1] ^= 0xff
				return data
			},
		},
		{
			name: "truncated",
			mutate: func(data []byte) []byte {
				return data[:len(data)-1]
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spent.db")
			store, err := OpenFileSpentSet(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			lockPath := path + ".lock"
			data, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatalf("ReadFile lock: %v", err)
			}
			if err := os.WriteFile(lockPath, tt.mutate(data), 0o600); err != nil {
				t.Fatalf("WriteFile lock: %v", err)
			}
			if reopened, err := OpenFileSpentSet(path); err == nil {
				_ = reopened.Close()
				t.Fatal("opened a redemption journal with a corrupt lock identity")
			}
		})
	}
}

func TestFileSpentSetPermissionChangesFailClosed(t *testing.T) {
	for _, target := range []string{"journal", "lock"} {
		t.Run(target, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spent.db")
			store := openSpentFile(t, path)
			changedPath := path
			if target == "lock" {
				changedPath += ".lock"
			}
			if err := os.Chmod(changedPath, 0o640); err != nil {
				t.Fatalf("Chmod %s: %v", target, err)
			}
			if err := store.Spend(spentToken(12)); err == nil || !strings.Contains(err.Error(), "failed closed") {
				t.Fatalf("Spend after %s permission change = %v, want fail-closed error", target, err)
			}
			if err := os.Chmod(changedPath, 0o600); err != nil {
				t.Fatalf("restore %s mode: %v", target, err)
			}
			if err := store.Spend(spentToken(13)); err == nil || !strings.Contains(err.Error(), "integrity failure") {
				t.Fatalf("Spend after restoring %s mode = %v, want sticky integrity error", target, err)
			}
		})
	}
}

func TestFileSpentSetJournalReplacementFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store := openSpentFile(t, path)
	if err := store.Spend(spentToken(14)); err != nil {
		t.Fatalf("initial Spend: %v", err)
	}

	original := path + ".original"
	if err := os.Rename(path, original); err != nil {
		t.Fatalf("rename journal: %v", err)
	}
	if err := os.WriteFile(path, []byte(spentFileHeader), 0o600); err != nil {
		t.Fatalf("write replacement journal: %v", err)
	}

	if err := store.Spend(spentToken(15)); err == nil || !strings.Contains(err.Error(), "replaced while open") {
		t.Fatalf("active Spend after replacement = %v, want replacement rejection", err)
	}
	if reopened, err := OpenFileSpentSet(path); err == nil {
		_ = reopened.Close()
		t.Fatal("opened a replacement journal despite the pinned identity")
	} else if !strings.Contains(err.Error(), "pins") {
		t.Fatalf("reopen replacement error = %v, want pinned-identity rejection", err)
	}
}

func TestFileSpentSetLockReplacementFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store := openSpentFile(t, path)

	original := path + ".lock.original"
	if err := os.Rename(path+".lock", original); err != nil {
		t.Fatalf("rename lock: %v", err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatalf("write replacement lock: %v", err)
	}
	if err := store.Spend(spentToken(16)); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("Spend after lock replacement = %v, want replacement rejection", err)
	}
}

func TestFileSpentSetDoesNotRecreateMissingPinnedJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	store, err := OpenFileSpentSet(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove journal: %v", err)
	}
	if reopened, err := OpenFileSpentSet(path); err == nil {
		_ = reopened.Close()
		t.Fatal("recreated a missing journal despite the pinned identity")
	} else if !strings.Contains(err.Error(), "lock identity remains") {
		t.Fatalf("reopen missing journal error = %v, want retained-identity rejection", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal was recreated after rejection: Stat error = %v", err)
	}
}

func TestFileSpentSetRefusesExistingEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spent.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if store, err := OpenFileSpentSet(path); err == nil {
		store.Close()
		t.Fatal("treated an existing empty journal as a new database")
	}
}
