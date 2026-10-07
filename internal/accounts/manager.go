package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"xgift/internal/proxy"
	"xgift/internal/store"
)

// RecordName is the record that stores the account pool.
const RecordName = "accounts"

// Manager starts and caches one embedded sing-box instance per account. The
// instance exposes a loopback mixed proxy; every HTTP request for that account
// is routed through it, so X sessions and their exit IP never mix.
type Manager struct {
	ctx     context.Context
	mu      sync.Mutex
	entries map[string]*entry
	health  *healthState
}

type entry struct {
	fingerprint string
	client      *http.Client
	close       func()
}

func NewManager(ctx context.Context) *Manager {
	return &Manager{ctx: ctx, entries: map[string]*entry{}, health: newHealthState()}
}

// Client returns an HTTP client bound to the account's proxy. A changed proxy
// configuration transparently restarts that account's tunnel.
func (m *Manager) Client(a Account) (*http.Client, error) {
	if err := a.Proxy.Validate(); err != nil {
		return nil, err
	}
	fingerprint := a.Proxy.Fingerprint()
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[a.ID]; e != nil {
		if e.fingerprint == fingerprint {
			return e.client, nil
		}
		e.close()
		delete(m.entries, a.ID)
	}
	outbound, err := a.Proxy.Outbound()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"outbounds": []json.RawMessage{outbound}})
	if err != nil {
		return nil, errors.New("cannot encode account proxy")
	}
	defer clear(raw)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot reserve a local proxy port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	instance, err := proxy.Start(m.ctx, raw, port)
	if err != nil {
		return nil, fmt.Errorf("cannot start proxy for account %s: %w", a.Label, err)
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(p), TLSHandshakeTimeout: 15 * time.Second, ForceAttemptHTTP2: true}
	client := &http.Client{Transport: transport, Timeout: 35 * time.Second}
	m.entries[a.ID] = &entry{fingerprint: fingerprint, client: client, close: func() { transport.CloseIdleConnections(); instance.Close() }}
	return client, nil
}

// Check verifies the proxy can actually reach x.com. It is used by the setup
// wizard and the CLI; it never sends credentials.
func (m *Manager) Check(ctx context.Context, a Account) error {
	client, err := m.Client(a)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://x.com", nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("proxy is running, but HTTPS to X failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 500 {
		return fmt.Errorf("X responded with HTTP %d through the proxy", res.StatusCode)
	}
	return nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.entries {
		e.close()
		delete(m.entries, id)
	}
}

// Load reads the account pool. Legacy single-account installs
// (cookies + a standalone shadowsocks outbound) are migrated once.
func Load(v *store.Store) ([]Account, error) {
	raw, err := v.Get(RecordName)
	if err == nil {
		defer clear(raw)
		return Parse(raw)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	accounts, migrateErr := migrateLegacy(v)
	if migrateErr != nil {
		return nil, migrateErr
	}
	if accounts == nil {
		return nil, errors.New("account pool is missing; store it with `xgift accounts add` or put --name accounts")
	}
	if err = Save(v, accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

func Save(v *store.Store, list []Account) error {
	if len(list) > 64 {
		return errors.New("at most 64 X accounts are supported")
	}
	seen := map[string]bool{}
	for i := range list {
		if err := list[i].Validate(); err != nil {
			return err
		}
		if seen[list[i].ID] {
			return fmt.Errorf("duplicate account id %s", list[i].ID)
		}
		seen[list[i].ID] = true
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put(RecordName, b)
}

// migrateLegacy converts the pre-pool records into one account. It refuses to
// guess: unless the old proxy record is exactly one shadowsocks outbound the
// operator must configure the pool explicitly.
func migrateLegacy(v *store.Store) ([]Account, error) {
	cookies, err := v.Get("cookies")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(cookies)
	var state struct {
		Cookies []struct{ Name, Value string }
	}
	if json.Unmarshal(cookies, &state) != nil {
		return nil, errors.New("legacy cookies record is invalid; configure the account pool explicitly")
	}
	auth, ct0 := "", ""
	for _, c := range state.Cookies {
		switch c.Name {
		case "auth_token":
			auth = c.Value
		case "ct0":
			ct0 = c.Value
		}
	}
	if auth == "" || ct0 == "" {
		return nil, errors.New("legacy cookies record has no auth_token/ct0; configure the account pool explicitly")
	}
	rawProxy, err := v.Get("proxy")
	if err != nil {
		return nil, errors.New("legacy install has no proxy record; configure the account pool explicitly")
	}
	defer clear(rawProxy)
	p, err := legacyProxy(rawProxy)
	if err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	return []Account{{ID: id, Label: "默认账号", AuthToken: auth, CT0: ct0, Enabled: true, Proxy: p}}, nil
}

func legacyProxy(raw []byte) (Proxy, error) {
	var config struct {
		Outbounds []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
			Method     string `json:"method"`
			Password   string `json:"password"`
		} `json:"outbounds"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return Proxy{}, errors.New("legacy proxy record is invalid; configure the account pool explicitly")
	}
	var found *Proxy
	for _, o := range config.Outbounds {
		if o.Type != "shadowsocks" || o.Tag == "direct" {
			continue
		}
		if found != nil {
			return Proxy{}, errors.New("legacy proxy record has several nodes; configure the account pool explicitly")
		}
		found = &Proxy{Type: "shadowsocks", Server: o.Server, ServerPort: o.ServerPort, Method: o.Method, Password: o.Password}
	}
	if found == nil {
		return Proxy{}, errors.New("legacy proxy record is not a single shadowsocks node; configure the account pool explicitly")
	}
	if err := found.Validate(); err != nil {
		return Proxy{}, fmt.Errorf("legacy proxy record: %w", err)
	}
	return *found, nil
}

// Select chooses the account that can create a new checkout soonest. The
// returned wait is zero when a slot is immediately available.
func Select(list []Account, wait func(Account, time.Time) (time.Duration, error), now time.Time) (Account, time.Duration, error) {
	var best Account
	var bestWait time.Duration
	found := false
	for _, a := range list {
		if !a.Enabled {
			continue
		}
		w, err := wait(a, now)
		if err != nil {
			return Account{}, 0, err
		}
		if !found || w < bestWait {
			best, bestWait, found = a, w, true
		}
	}
	if !found {
		return Account{}, 0, ErrNoEnabledAccount
	}
	return best, bestWait, nil
}

// Sanitize strips credentials from an account before it is returned to the
// admin UI. The returned map never contains auth_token, ct0 or the proxy
// password; only a presence flag and a non-secret fingerprint.
func Sanitize(a Account) map[string]any {
	return map[string]any{
		"id":          a.ID,
		"label":       a.Label,
		"enabled":     a.Enabled,
		"has_cookie":  a.AuthToken != "" && a.CT0 != "",
		"proxy":       map[string]any{"type": "shadowsocks", "server": a.Proxy.Server, "server_port": a.Proxy.ServerPort, "method": a.Proxy.Method},
		"fingerprint": a.Proxy.Fingerprint(),
	}
}

// Redact returns a copy safe for logs.
func Redact(a Account) string {
	label := a.Label
	if label == "" {
		label = a.ID
	}
	return fmt.Sprintf("%s (%s, %s:%d, %s)", label, a.ID, a.Proxy.Server, a.Proxy.ServerPort, a.Proxy.Method)
}
