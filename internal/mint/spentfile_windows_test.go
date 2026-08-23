//go:build windows

package mint

import (
	"errors"
	"testing"
)

func TestWindowsSpentStoreFailsClosed(t *testing.T) {
	store, err := OpenFileSpentSet(t.TempDir() + `\spent.db`)
	if store != nil || !errors.Is(err, errWindowsSpentStore) {
		t.Fatalf("OpenFileSpentSet = (%v, %v), want explicit unsupported error", store, err)
	}
}
