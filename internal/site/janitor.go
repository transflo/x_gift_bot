package site

import (
	"log"
	"time"
)

// Retention windows. These are records the system wrote for its own use, never
// the product: a code's encrypted value, an order binding, an account's
// throttle slot and the single API credential are all kept forever.
//
// The balance each one strikes:
//
//   - a payment-window snapshot is only needed while that session could still
//     complete, and a public link lives three minutes, so 30 days is generous.
//   - a completion record is the audit trail that a payment settled; it is the
//     one an operator might need months later, hence 90 days.
//   - Stripe error diagnostics are triage material with a short useful life.
var retentionRules = []struct {
	Prefix string
	Label  string
	Days   int
}{
	{"checkout-verification:", "付款会话快照", 30},
	{"checkout-completion:", "付款完成凭证", 90},
	{"checkout-history:", "换链接历史", 90},
	{"stripe-error:", "Stripe 错误诊断", 30},
}

// maintenanceInterval is deliberately slow: this runs against live tables, and
// an installation generating enough traffic to need more frequent pruning would
// rather not have the vacuum competing with checkouts.
const maintenanceInterval = 24 * time.Hour

type retentionResult struct {
	At       int64          `json:"at"`
	Removed  int            `json:"removed"`
	ByPrefix map[string]int `json:"by_prefix,omitempty"`
	Vacuumed bool           `json:"vacuumed"`
	LogBytes int64          `json:"log_bytes"`
	Err      string         `json:"error,omitempty"`
}

// maintain applies the retention windows, reclaims space, and truncates the
// write-ahead log. Safe to call directly from the admin panel.
func (s *server) maintain() retentionResult {
	now := time.Now()
	result := retentionResult{At: now.Unix(), ByPrefix: map[string]int{}}
	for _, rule := range retentionRules {
		before, err := s.vault.CountPrefixes(rule.Prefix)
		if err != nil {
			result.Err = "无法统计保管库记录"
			break
		}
		removed, err := s.vault.PrunePrefixes(pruneCutoff(now, rule.Days), rule.Prefix)
		if err != nil {
			result.Err = "无法删除过期记录"
			break
		}
		if removed == 0 {
			continue
		}
		result.Removed += removed
		// Report the delta rather than the raw count so the number matches what
		// the operator sees disappear from the storage table.
		result.ByPrefix[rule.Prefix] = min(removed, before[rule.Prefix].Records)
	}
	if result.Err == "" {
		vacuumed, err := s.vault.Compact()
		if err != nil {
			result.Err = "无法回收保管库空间"
		}
		result.Vacuumed = vacuumed
	}
	// The write-ahead log is otherwise only checkpointed when SQLite decides to,
	// so a long-lived process can accumulate one well past the database size.
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil && result.Err == "" {
		result.Err = "无法截断数据库日志"
	}
	if s.logs != nil {
		for _, f := range s.logs.Files() {
			result.LogBytes += f.Bytes
		}
	}
	s.retention.record(result)
	if result.Removed > 0 || result.LogBytes > 0 {
		log.Printf("维护完成：清理 %d 条过期记录，日志占用 %d 字节", result.Removed, result.LogBytes)
	}
	return result
}

// maintenanceLoop runs the janitor on first start and then daily. The first run
// is delayed so a container that restarts in a loop is not pruning on every
// boot, and so it never races the startup burst of checkout work.
func (s *server) maintenanceLoop() {
	select {
	case <-s.ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}
	s.maintain()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.maintain()
		}
	}
}
