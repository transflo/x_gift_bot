package oplog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Log {
	t.Helper()
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestEventRoundTripsThroughTail(t *testing.T) {
	l := openTest(t)
	l.Event(Entry{Kind: "http", Level: "warn", IP: "203.0.113.5", Method: "POST", Path: "/api/redeem", Status: 429, MS: 12})
	l.Event(Entry{Kind: "log", Level: "info", Message: "维护完成"})

	entries, err := l.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	first := entries[0]
	if first.IP != "203.0.113.5" || first.Status != 429 || first.Path != "/api/redeem" {
		t.Errorf("access fields did not survive the round trip: %+v", first)
	}
	if entries[1].Message != "维护完成" {
		t.Errorf("message = %q, want 维护完成", entries[1].Message)
	}
}

func TestTailReturnsNewestEntries(t *testing.T) {
	l := openTest(t)
	for _, message := range []string{"one", "two", "three", "four"} {
		l.Event(Entry{Kind: "log", Message: message})
	}
	entries, err := l.Tail(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Message != "three" || entries[1].Message != "four" {
		t.Fatalf("tail = %+v, want the last two in order", entries)
	}
}

// The standard logger writes free text, not JSON; the adapter has to wrap it so
// one download contains both sources.
func TestWriterWrapsPlainLogLines(t *testing.T) {
	l := openTest(t)
	if _, err := l.Writer().Write([]byte("order abc link generation failed: no_account\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := l.Tail(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Kind != "log" || entries[0].Level != "error" {
		t.Errorf("entry = %+v, want a log entry classified as an error", entries[0])
	}
	if strings.HasSuffix(entries[0].Message, "\n") {
		t.Error("the trailing newline should be stripped")
	}
}

// Rotation is what stops a long-running installation from filling its disk with
// its own history, so the bound has to hold under a write pattern far larger
// than the threshold.
func TestRotationBoundsFileCount(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.maxBytes = 2048
	l.keep = 3
	payload := strings.Repeat("x", 512)
	for i := 0; i < 60; i++ {
		l.Event(Entry{Kind: "log", Message: payload})
	}
	files := l.Files()
	if len(files) > l.keep+1 {
		t.Fatalf("%d log files exist; the cap is %d", len(files), l.keep+1)
	}
	var total int64
	for _, f := range files {
		total += f.Bytes
	}
	if limit := int64(l.maxBytes) * int64(l.keep+1); total > limit {
		t.Errorf("logs occupy %d bytes, above the %d byte bound", total, limit)
	}
	// The newest entries must still be readable after a rotation.
	entries, err := l.Tail(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("tail after rotation = %d entries, want 1", len(entries))
	}
}

// The download endpoint takes a filename from a query parameter, so the reader
// itself has to refuse anything the logger does not own.
func TestOpenFileRejectsForeignNames(t *testing.T) {
	l := openTest(t)
	for _, name := range []string{"../../etc/passwd", "vault-password", "vault.db", "xgift.log.bak", "/etc/shadow"} {
		if f, _, err := l.OpenFile(name); err == nil {
			f.Close()
			t.Errorf("OpenFile(%q) should have been refused", name)
		}
	}
	f, _, err := l.OpenFile(filepath.Base(l.Path()))
	if err != nil {
		t.Fatalf("the active file should be readable: %v", err)
	}
	f.Close()
}

// A partially written final line must not hide the entries before it.
func TestTailSkipsMalformedLines(t *testing.T) {
	l := openTest(t)
	l.Event(Entry{Kind: "log", Message: "good"})
	file, err := os.OpenFile(l.Path(), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(`{"kind":"log","msg":"trunc`); err != nil {
		t.Fatal(err)
	}
	file.Close()

	entries, err := l.Tail(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Message != "good" {
		t.Fatalf("entries = %+v, want just the complete line", entries)
	}
}

func TestEntryJSONUsesShortFieldNames(t *testing.T) {
	raw, err := json.Marshal(Entry{Kind: "http", IP: "203.0.113.5", Status: 200})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind", "ip", "status"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("expected field %q in %s", key, raw)
		}
	}
	if _, ok := fields["user_agent"]; ok {
		t.Error("empty fields should be omitted rather than written as null")
	}
}
