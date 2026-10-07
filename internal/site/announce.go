package site

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"xgift/internal/oplog"
)

// announcementKey is the record holding the operator's banner. It lives
// outside site.db so a backup of the record store carries the site's
// configuration with it.
const announcementKey = "announcement"

// maxAnnouncementRunes keeps the banner to a single readable line on a phone.
const maxAnnouncementRunes = 200

type announcement struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
	Level   string `json:"level"`
	Updated int64  `json:"updated"`
}

// announcementLevels are the banner tones the stylesheet knows how to paint.
var announcementLevels = map[string]bool{"info": true, "warning": true, "critical": true}

func readAnnouncement(v interface {
	Get(string) ([]byte, error)
}) (announcement, error) {
	raw, err := v.Get(announcementKey)
	if err != nil {
		return announcement{}, err
	}
	defer clear(raw)
	var a announcement
	if err = json.Unmarshal(raw, &a); err != nil {
		return announcement{}, err
	}
	if !announcementLevels[a.Level] {
		a.Level = "info"
	}
	return a, nil
}

// normalizeAnnouncement validates operator input. Control characters are
// rejected rather than stripped: the banner is rendered into both pages, and
// silently changing what someone typed is worse than asking them to fix it.
func normalizeAnnouncement(a announcement) (announcement, error) {
	a.Text = strings.TrimSpace(a.Text)
	if !announcementLevels[a.Level] {
		a.Level = "info"
	}
	if a.Enabled {
		if a.Text == "" {
			return a, errors.New("公告内容不能为空")
		}
		if len([]rune(a.Text)) > maxAnnouncementRunes {
			return a, errors.New("公告内容不能超过 200 个字符")
		}
		if strings.ContainsFunc(a.Text, func(r rune) bool {
			return r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r)
		}) {
			return a, errors.New("公告只能是一行文本")
		}
	}
	return a, nil
}

// announcement is the public read. An unconfigured or disabled banner returns
// enabled=false rather than 404, so the page can decide without treating a
// normal state as an error.
func (s *server) announcement(w http.ResponseWriter, r *http.Request) {
	current, err := readAnnouncement(s.records)
	if err != nil || !current.Enabled {
		reply(w, 200, map[string]any{"enabled": false})
		return
	}
	reply(w, 200, map[string]any{"enabled": true, "text": current.Text, "level": current.Level, "updated": current.Updated})
}

func (s *server) saveAnnouncement(w http.ResponseWriter, r *http.Request) {
	var q announcement
	if !decode(w, r, &q) {
		return
	}
	next, err := normalizeAnnouncement(q)
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	next.Updated = time.Now().Unix()
	raw, err := json.Marshal(next)
	if err != nil {
		message(w, 503, "公告保存失败。")
		return
	}
	if err = s.records.Put(announcementKey, raw); err != nil {
		message(w, 503, "公告保存失败，请稍后重试。")
		return
	}
	s.logs.Event(oplog.Entry{Kind: "admin", Level: "info", IP: clientIP(r), Message: "announcement updated", Extra: map[string]any{"enabled": next.Enabled, "level": next.Level, "length": len([]rune(next.Text))}})
	reply(w, 200, map[string]any{"announcement": next, "message": "公告已更新。"})
}
