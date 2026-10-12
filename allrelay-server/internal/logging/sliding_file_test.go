package logging

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSlidingFileRetainsNewestCompleteLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "remote.log")
	writer, err := NewSlidingFileWriter(path, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for i := range 100 {
		line := []byte(fmt.Sprintf("record-%03d\n", i))
		if n, err := writer.Write(line); err != nil || n != len(line) {
			t.Fatalf("write record %d: n=%d err=%v", i, n, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 128 || !bytes.HasSuffix(data, line) {
			t.Fatalf("retained log exceeds ceiling or loses latest record: %q", data)
		}
		for _, retained := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
			if len(retained) != len("record-000") || !bytes.HasPrefix(retained, []byte("record-")) {
				t.Fatalf("partial retained record: %q", retained)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "remote.log" {
		t.Fatalf("unexpected rotated log files: %v", entries)
	}
}

func TestSlidingFileRetainsTailOfOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote.log")
	writer, err := NewSlidingFileWriter(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	record := append(bytes.Repeat([]byte("x"), 100), []byte("latest\n")...)
	if _, err := writer.Write(record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 32 || !bytes.HasSuffix(data, []byte("latest\n")) {
		t.Fatalf("oversized record tail lost: %q", data)
	}
}
