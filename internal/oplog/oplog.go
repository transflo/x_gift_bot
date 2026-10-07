// Package oplog writes the operator-visible log: one JSON object per line, so
// the admin panel can render it as a table and a download is directly
// analysable. It rotates by size, which is what keeps a long-running
// installation from filling its disk with its own history.
package oplog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry is one log line. Field names are short because there are a lot of them
// and the file is meant to be downloaded and read.
type Entry struct {
	Time    int64          `json:"t"`
	Kind    string         `json:"kind"`
	Level   string         `json:"level,omitempty"`
	Message string         `json:"msg,omitempty"`
	IP      string         `json:"ip,omitempty"`
	Method  string         `json:"method,omitempty"`
	Path    string         `json:"path,omitempty"`
	Status  int            `json:"status,omitempty"`
	MS      int64          `json:"ms,omitempty"`
	UA      string         `json:"ua,omitempty"`
	Referer string         `json:"referer,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

type Log struct {
	path     string
	maxBytes int64
	keep     int
	mu       sync.Mutex
	file     *os.File
	size     int64
}

const (
	defaultMaxBytes = 8 << 20
	defaultKeep     = 4
	// tailBytes bounds how much of the file the reader is willing to walk
	// backwards for one request.
	tailBytes = 4 << 20
)

// Open prepares the log inside dir, reopening an existing file when it is still
// under the rotation threshold.
func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	l := &Log{path: filepath.Join(dir, "xgift.log"), maxBytes: defaultMaxBytes, keep: defaultKeep}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Log) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file, l.size = f, info.Size()
	return nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Path is the active file. Callers must not assume it exists after rotation.
func (l *Log) Path() string { return l.path }

// Event appends one structured entry. Logging never fails a request: a full or
// unreadable disk is reported to stderr and otherwise ignored.
func (l *Log) Event(e Entry) {
	if e.Time == 0 {
		e.Time = time.Now().Unix()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.write(append(line, '\n'))
}

func (l *Log) write(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	if l.size+int64(len(b)) > l.maxBytes {
		if err := l.rotate(); err != nil {
			return
		}
	}
	n, err := l.file.Write(b)
	l.size += int64(n)
	if err != nil {
		// The next write retries; surfacing this on stderr is the only signal
		// available without recursing into the logger.
		os.Stderr.WriteString("xgift: log write failed: " + err.Error() + "\n")
	}
}

// rotate closes the active file and shifts the numbered backups, dropping the
// oldest. The total footprint is bounded by maxBytes * keep.
func (l *Log) rotate() error {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	for i := l.keep - 1; i >= 1; i-- {
		older := numbered(l.path, i)
		if _, err := os.Stat(older); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(older, numbered(l.path, i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(l.path, numbered(l.path, 1)); err != nil {
		return err
	}
	return l.open()
}

func numbered(path string, i int) string {
	if i == 0 {
		return path
	}
	return path + "." + strconv.Itoa(i)
}

// Writer adapts the standard logger. Everything the process already reports
// through log.Printf lands in the same downloadable file, so an operator
// debugging a checkout failure does not have to choose between two sources.
func (l *Log) Writer() io.Writer { return &logWriter{l: l} }

type logWriter struct{ l *Log }

func (w *logWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg != "" {
		w.l.Event(Entry{Kind: "log", Level: levelOf(msg), Message: msg})
	}
	return len(p), nil
}

// levelOf classifies the process's own messages so the panel can highlight the
// ones worth reading. The heuristic only has to be right for lines this program
// actually writes.
func levelOf(msg string) string {
	lower := strings.ToLower(msg)
	for _, marker := range []string{"failed", "error", "refusing", "not ready", "unusable", "could not"} {
		if strings.Contains(lower, marker) {
			return "error"
		}
	}
	return "info"
}

// Files lists the log files that exist, newest first, with their sizes.
func (l *Log) Files() []File {
	out := []File{}
	for i := 0; i <= l.keep; i++ {
		path := numbered(l.path, i)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, File{Name: filepath.Base(path), Bytes: info.Size(), Modified: info.ModTime().Unix(), Active: i == 0})
	}
	return out
}

type File struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Modified int64  `json:"modified"`
	Active   bool   `json:"active"`
}

// Tail returns up to n of the most recent entries, newest last. Lines that do
// not parse are skipped rather than failing the read, so a half-written final
// line never hides the rest.
func (l *Log) Tail(n int) ([]Entry, error) {
	if n < 1 || n > 2000 {
		n = 200
	}
	raw, err := l.tailBytes(int64(n)*512, tailBytes)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(raw, []byte{'\n'})
	entries := make([]Entry, 0, n)
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

// tailBytes reads at most want bytes from the end of the file, widened to the
// previous newline so the first line is never cut in half.
func (l *Log) tailBytes(want, limit int64) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if want > limit {
		want = limit
	}
	if size == 0 {
		return nil, nil
	}
	// Read a little past the window so the widening read has something to find.
	read := want + 4096
	if read > size {
		read = size
	}
	buf := make([]byte, read)
	if _, err = f.ReadAt(buf, size-read); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if size == read {
		return buf, nil
	}
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		return buf[i+1:], nil
	}
	return buf, nil
}

// OpenFile returns a reader for one named log file. Only names this log owns
// are accepted, so the admin download endpoint cannot be pointed at the vault.
func (l *Log) OpenFile(name string) (*os.File, int64, error) {
	if name == "" {
		name = filepath.Base(l.path)
	}
	if strings.ContainsAny(name, `/\`) || !strings.HasPrefix(name, filepath.Base(l.path)) {
		return nil, 0, errors.New("unknown log file")
	}
	known := false
	for _, f := range l.Files() {
		if f.Name == name {
			known = true
			break
		}
	}
	if !known {
		return nil, 0, errors.New("unknown log file")
	}
	f, err := os.Open(filepath.Join(filepath.Dir(l.path), name))
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}
