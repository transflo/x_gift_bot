package accounts

import (
	"context"
	"log"
	"sync"
	"time"

	"xgift/internal/vault"
)

// Health is what the pool knows about an account without asking X: the outcome
// of the last probe through that account's proxy.
type Health struct {
	OK        bool      `json:"ok"`
	Fails     int       `json:"fails"`
	CheckedAt time.Time `json:"checked_at"`
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
}

// unhealthyAfter is how many consecutive probe failures retire an account from
// selection. A single failure is usually a blip; three in a row means the proxy
// or the cookie is genuinely broken.
const unhealthyAfter = 3

// health holds probe results. It is separate from the proxy cache mutex so a
// slow probe never blocks request routing.
type healthState struct {
	mu      sync.Mutex
	entries map[string]*Health
}

func newHealthState() *healthState {
	return &healthState{entries: map[string]*Health{}}
}

// Record stores one probe outcome. Success clears the failure streak.
func (h *healthState) Record(id string, err error, latency time.Duration) Health {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[id]
	if e == nil {
		e = &Health{}
		h.entries[id] = e
	}
	e.CheckedAt = time.Now()
	e.LatencyMS = latency.Milliseconds()
	if err != nil {
		e.OK = false
		e.Fails++
		e.Error = err.Error()
	} else {
		e.OK = true
		e.Fails = 0
		e.Error = ""
	}
	return *e
}

// HealthOf returns the last recorded state; the zero value means "never probed",
// which is treated as usable so a fresh install is not blocked.
func (m *Manager) HealthOf(id string) Health {
	m.health.mu.Lock()
	defer m.health.mu.Unlock()
	if e := m.health.entries[id]; e != nil {
		return *e
	}
	return Health{}
}

// Usable reports whether the account may still be selected. Accounts that have
// never been probed are usable, and so is one whose probe is merely stale — a
// stale success should not retire an account that has been working.
func (m *Manager) Usable(id string) bool {
	h := m.HealthOf(id)
	return h.Fails < unhealthyAfter
}

// MarkFailed records an account-side failure observed during a real request, so
// the pool learns about a dead proxy without waiting for the next probe.
func (m *Manager) MarkFailed(id string, err error) {
	h := m.health.Record(id, err, 0)
	if h.Fails == unhealthyAfter {
		log.Printf("账号 %s 连续失败 %d 次，暂停用于新订单：%v", id, h.Fails, err)
	}
}

// MarkUsable records a success observed during a real request.
func (m *Manager) MarkUsable(id string, latency time.Duration) {
	m.health.Record(id, nil, latency)
}

// Probe checks one account and records the outcome.
func (m *Manager) Probe(ctx context.Context, a Account) error {
	start := time.Now()
	err := m.Check(ctx, a)
	m.health.Record(a.ID, err, time.Since(start))
	return err
}

// Watch probes every enabled account on an interval until ctx is done, so a
// proxy that dies between checkouts is noticed before a customer hits it.
func (m *Manager) Watch(ctx context.Context, v *vault.Vault, every time.Duration) {
	if every <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probeAll(ctx, v)
		}
	}
}

func (m *Manager) probeAll(ctx context.Context, v *vault.Vault) {
	list, err := Load(v)
	if err != nil {
		return
	}
	for _, a := range list {
		if !a.Enabled || ctx.Err() != nil {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		probeErr := m.Probe(probeCtx, a)
		cancel()
		if probeErr != nil {
			log.Printf("测活失败 %s：%v", Redact(a), probeErr)
		}
	}
}
