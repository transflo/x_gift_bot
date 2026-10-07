package site

import (
	"net/http"
	"strconv"
	"time"
)

// readLogs returns the most recent entries for the panel's live view. The file
// is read on demand rather than held in memory: an installation running for
// months should not pay for a log buffer it never opens.
func (s *server) readLogs(w http.ResponseWriter, r *http.Request) {
	lines := 300
	if raw := r.URL.Query().Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 2000 {
			message(w, 400, "行数需要在 1 到 2000 之间。")
			return
		}
		lines = n
	}
	level := r.URL.Query().Get("level")
	if level != "" && level != "info" && level != "warn" && level != "error" {
		message(w, 400, "日志级别不正确。")
		return
	}
	entries, err := s.logs.Tail(lines * 2)
	if err != nil {
		message(w, 503, "无法读取日志文件。")
		return
	}
	if level != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Level == level {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	if len(entries) > lines {
		entries = entries[len(entries)-lines:]
	}
	// Newest first: an operator opening the panel is looking for what just
	// happened, not for the state of the world an hour ago.
	reversed := make([]any, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		reversed = append(reversed, entries[i])
	}
	reply(w, 200, map[string]any{"entries": reversed, "files": s.logs.Files()})
}

// downloadLog streams one log file as an attachment. Names are validated
// against the files the logger owns, so this endpoint cannot be walked onto the
// record store or the site database.
func (s *server) downloadLog(w http.ResponseWriter, r *http.Request) {
	f, size, err := s.logs.OpenFile(r.URL.Query().Get("file"))
	if err != nil {
		message(w, 404, "日志文件不存在。")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="xgift.log"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	http.ServeContent(w, r, "xgift.log", time.Time{}, f)
}
