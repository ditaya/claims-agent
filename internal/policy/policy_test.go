package policy

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func seedPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine caller")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "data", "policies.json")
}

func TestLoadSeedData(t *testing.T) {
	store, err := Load(seedPath(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := store.Len(), 10; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestLookupFound(t *testing.T) {
	store, err := Load(seedPath(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	p, err := store.Lookup("POL-1001")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if p.HolderName != "Jane Doe" {
		t.Errorf("HolderName = %q, want %q", p.HolderName, "Jane Doe")
	}
	if p.CoverageType != "auto" {
		t.Errorf("CoverageType = %q, want %q", p.CoverageType, "auto")
	}
}

func TestLookupNotFound(t *testing.T) {
	store, err := Load(seedPath(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = store.Lookup("POL-9999")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup() error = %v, want ErrNotFound", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/policies.json")
	if err == nil {
		t.Error("Load() expected error for missing file, got nil")
	}
}
