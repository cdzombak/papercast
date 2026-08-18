package instapaper

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instapaper.json")
	want := &Credentials{Token: "tok", TokenSecret: "sec"}
	if err := SaveCredentials(path, want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}

	got, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if *got != *want {
		t.Errorf("LoadCredentials = %+v, want %+v", got, want)
	}
}

func TestLoadCredentialsMissingFile(t *testing.T) {
	_, err := LoadCredentials(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want wrapped fs.ErrNotExist", err)
	}
}

func TestSaveCredentialsMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "instapaper.json")
	err := SaveCredentials(path, &Credentials{Token: "t", TokenSecret: "s"})
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want wrapped fs.ErrNotExist", err)
	}
}
