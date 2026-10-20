//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "list.json")
	if err := writeFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
	// A folder that cannot be created: the error reaches the caller and the
	// old file is untouched.
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(filepath.Join(blocker, "list.json"), []byte("x")); err == nil {
		t.Fatal("no error for an impossible path")
	}
	// A locked target: the rename fails, the old content stays.
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = writeFileAtomic(path, []byte("three"))
	f.Close()
	if b, _ := os.ReadFile(path); err == nil && string(b) != "three" || err != nil && string(b) != "two" {
		t.Fatalf("err %v, content %q", err, b)
	}
}
