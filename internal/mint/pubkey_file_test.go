package mint

import (
	"crypto/rsa"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWritePublicKeyReplacesTheFileWhole(t *testing.T) {
	a, err := GenerateKey(MinKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateKey(MinKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mint.pub")

	for i, priv := range []*rsa.PrivateKey{a, b} {
		if err := WritePublicKey(&priv.PublicKey, path); err != nil {
			t.Fatalf("write %d: %v", i+1, err)
		}
		got, err := LoadPublicKey(path)
		if err != nil || KeyID(got) != KeyID(&priv.PublicKey) {
			t.Fatalf("write %d: loaded %v, %v", i+1, got, err)
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v, %v; want 0644 so gateway operators can read it", info.Mode().Perm(), err)
		}
	}

	// A refused write must leave the published key as it was, with no
	// temporary file beside it.
	if err := WritePublicKey(nil, path); err == nil {
		t.Fatal("wrote a nil key")
	}
	if got, err := LoadPublicKey(path); err != nil || KeyID(got) != KeyID(&b.PublicKey) {
		t.Fatalf("after a refused write: %v, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %q, want only mint.pub", names)
	}
}

// A gateway may read the published key while the mint rewrites it at start.
// It must always find a whole key, never an empty or partial file.
func TestPublicKeyIsNeverSeenHalfWritten(t *testing.T) {
	a, err := GenerateKey(MinKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateKey(MinKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mint.pub")
	if err := WritePublicKey(&a.PublicKey, path); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var failures atomic.Int64
	var firstErr atomic.Value
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			if _, err := LoadPublicKey(path); err != nil {
				if failures.Add(1) == 1 {
					firstErr.Store(err)
				}
			}
		}
	}()
	for i := 0; i < 300; i++ {
		key := &a.PublicKey
		if i%2 == 1 {
			key = &b.PublicKey
		}
		if err := WritePublicKey(key, path); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if n := failures.Load(); n > 0 {
		t.Fatalf("a reader failed %d times while the key was rewritten; first: %v", n, firstErr.Load())
	}
}

func TestGenerateKeyRefusesSizesOutsideItsBoundsWithoutWaiting(t *testing.T) {
	for _, bits := range []int{0, MinKeyBits - 1, MaxKeyBits + 1, 30720, 1 << 20} {
		start := time.Now()
		if _, err := GenerateKey(bits); err == nil {
			t.Fatalf("GenerateKey(%d) succeeded", bits)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("refusing GenerateKey(%d) took %s; it must not start generating", bits, elapsed)
		}
	}
}
