package site

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"xgift/internal/accounts"
	"xgift/internal/checkout"
)

func (s *server) listAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := accounts.Load(s.records)
	if err != nil {
		// An unconfigured pool is reported as an empty list, not an error, so
		// the admin UI can bootstrap it.
		if !strings.Contains(err.Error(), "missing") {
			message(w, 503, "账号池读取失败，请检查加密记录。")
			return
		}
		list = []accounts.Account{}
	}
	sanitized := make([]map[string]any, 0, len(list))
	enabled := 0
	for _, a := range list {
		view := accounts.Sanitize(a)
		if h := s.accounts.HealthOf(a.ID); !h.CheckedAt.IsZero() {
			view["health"] = h
		}
		sanitized = append(sanitized, view)
		if a.Enabled {
			enabled++
		}
	}
	records := map[string]bool{}
	if _, err := s.records.Get("api-auth"); err == nil {
		records["api_auth"] = true
	}
	if key, err := s.records.Get("stripe-key"); err == nil {
		clear(key)
		records["stripe_key"] = true
	}
	if _, err := checkout.ReadCatalog(s.records); err == nil {
		records["catalog"] = true
	}
	reply(w, 200, map[string]any{"accounts": sanitized, "enabled": enabled, "records": records})
}

type accountPayload struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	AuthToken string `json:"auth_token"`
	CT0       string `json:"ct0"`
	Enabled   bool   `json:"enabled"`
	Proxy     struct {
		Type       string `json:"type"`
		Server     string `json:"server"`
		ServerPort int    `json:"server_port"`
		Method     string `json:"method"`
		Password   string `json:"password"`
	} `json:"proxy"`
}

func (s *server) saveAccount(w http.ResponseWriter, r *http.Request) {
	var q accountPayload
	if !decode(w, r, &q) {
		return
	}
	list, err := accounts.Load(s.records)
	if err != nil {
		if !strings.Contains(err.Error(), "missing") {
			message(w, 503, "账号池读取失败，请检查加密记录。")
			return
		}
		list = []accounts.Account{}
	}
	if q.Proxy.Type != "" && q.Proxy.Type != "shadowsocks" {
		message(w, 400, "代理仅支持 Shadowsocks。")
		return
	}
	index := -1
	for i := range list {
		if q.ID != "" && list[i].ID == q.ID {
			index = i
			break
		}
	}
	account := accounts.Account{
		ID:      q.ID,
		Label:   strings.TrimSpace(q.Label),
		Enabled: q.Enabled,
		Proxy: accounts.Proxy{
			Type:       "shadowsocks",
			Server:     strings.TrimSpace(q.Proxy.Server),
			ServerPort: q.Proxy.ServerPort,
			Method:     strings.TrimSpace(q.Proxy.Method),
			Password:   q.Proxy.Password,
		},
	}
	if index >= 0 {
		account.AuthToken, account.CT0 = list[index].AuthToken, list[index].CT0
	}
	if q.AuthToken != "" || q.CT0 != "" {
		account.AuthToken, account.CT0 = strings.TrimSpace(q.AuthToken), strings.TrimSpace(q.CT0)
	}
	if err = account.Validate(); err != nil {
		message(w, 400, accountErrorText(err))
		return
	}
	if index >= 0 {
		list[index] = account
	} else {
		if len(list) >= 64 {
			message(w, 400, "最多支持 64 个 X 账号。")
			return
		}
		list = append(list, account)
	}
	if err = accounts.Save(s.records, list); err != nil {
		message(w, 503, "账号保存失败，请稍后重试。")
		return
	}
	reply(w, 200, map[string]any{"account": accounts.Sanitize(account), "created": index < 0})
}

func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &q) {
		return
	}
	list, err := accounts.Load(s.records)
	if err != nil {
		message(w, 503, "账号池读取失败。")
		return
	}
	next := list[:0]
	removed := false
	for _, a := range list {
		if a.ID == q.ID {
			removed = true
			continue
		}
		next = append(next, a)
	}
	if !removed {
		message(w, 404, "账号不存在。")
		return
	}
	if len(next) == 0 {
		message(w, 409, "至少需要保留一个账号；请先添加新账号再删除。")
		return
	}
	if err = accounts.Save(s.records, next); err != nil {
		message(w, 503, "删除失败，请稍后重试。")
		return
	}
	message(w, 200, "账号已删除。")
}

func (s *server) toggleAccount(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if !decode(w, r, &q) {
		return
	}
	list, err := accounts.Load(s.records)
	if err != nil {
		message(w, 503, "账号池读取失败。")
		return
	}
	found := false
	for i := range list {
		if list[i].ID == q.ID {
			list[i].Enabled = q.Enabled
			found = true
			break
		}
	}
	if !found {
		message(w, 404, "账号不存在。")
		return
	}
	if err = accounts.Save(s.records, list); err != nil {
		message(w, 503, "保存失败，请稍后重试。")
		return
	}
	message(w, 200, "账号状态已更新。")
}

func (s *server) testAccount(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &q) {
		return
	}
	list, err := accounts.Load(s.records)
	if err != nil {
		message(w, 503, "账号池读取失败。")
		return
	}
	var account accounts.Account
	found := false
	for _, a := range list {
		if a.ID == q.ID {
			account, found = a, true
			break
		}
	}
	if !found {
		message(w, 404, "账号不存在。")
		return
	}
	if _, err = s.records.Get("api-auth"); err != nil {
		message(w, 409, "X API 授权记录缺失，请先运行配置向导。")
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err = s.accounts.Probe(ctx, account); err != nil {
		message(w, 502, "代理连通性检查失败："+safeUpstreamError(err))
		return
	}
	reply(w, 200, map[string]any{"ok": true, "message": "代理可正常访问 X。", "latency_ms": time.Since(started).Milliseconds()})
}

func accountErrorText(err error) string {
	switch {
	case errors.Is(err, accounts.ErrNoEnabledAccount):
		return "至少需要一个启用的账号。"
	default:
		return "账号配置无效：" + err.Error()
	}
}

func safeUpstreamError(err error) string {
	msg := err.Error()
	if len(msg) > 180 {
		msg = msg[:180]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, msg)
}
