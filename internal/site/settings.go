package site

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/oplog"
)

// usagePrefixes are the record families the storage report breaks down. The
// ones the janitor prunes are marked; the rest are the product itself, reported
// so an operator can watch a pool of unused codes grow rather than discovering
// it as a full disk.
var usagePrefixes = []struct {
	Prefix string
	Label  string
}{
	{"redemption:", "兑换码原文"},
	{"checkout:", "订单绑定"},
	{"checkout-completion:", "付款完成凭证"},
	{"checkout-verification:", "付款会话快照"},
	{"checkout-history:", "换链接历史"},
	{"stripe-error:", "Stripe 错误诊断"},
	{"checkout-creation:last:", "账号创建额度"},
	{"checkout-active:", "在途订单"},
}

type settingsResponse struct {
	StripeKey       stripeKeyView   `json:"stripe_key"`
	Catalog         catalogView     `json:"catalog"`
	PaymentsEnabled bool            `json:"payments_enabled"`
	Announcement    announcement    `json:"announcement"`
	Storage         storageReport   `json:"storage"`
	Retention       retentionReport `json:"retention"`
}

type stripeKeyView struct {
	Set     bool   `json:"set"`
	Key     string `json:"key"`
	Source  string `json:"source"`
	Invalid string `json:"invalid,omitempty"`
}

type catalogView struct {
	Available bool                   `json:"available"`
	Merchant  string                 `json:"merchant"`
	Currency  string                 `json:"currency"`
	Plans     []checkout.CatalogPlan `json:"plans"`
	Error     string                 `json:"error,omitempty"`
}

type storageFile struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type storageReport struct {
	FreeBytes    int64         `json:"free_bytes"`
	TotalBytes   int64         `json:"total_bytes"`
	Files        []storageFile `json:"files"`
	LogBytes     int64         `json:"log_bytes"`
	LogFiles     []oplog.File  `json:"log_files"`
	Records      []recordUsage `json:"records"`
	RecordsBytes int64         `json:"records_bytes"`
}

type recordUsage struct {
	Prefix  string `json:"prefix"`
	Label   string `json:"label"`
	Records int    `json:"records"`
	Bytes   int64  `json:"bytes"`
	Pruned  bool   `json:"pruned"`
	Days    int    `json:"days,omitempty"`
}

// readSettings answers "what is this installation actually running with" in one
// round trip: the active Stripe key and where it came from, the plans the
// catalog resolves to, the live banner, and what is on disk.
func (s *server) readSettings(w http.ResponseWriter, r *http.Request) {
	view := stripeKeyView{Source: "缺失"}
	if raw, err := s.vault.Get("stripe-key"); err == nil {
		key := strings.TrimSpace(string(raw))
		clear(raw)
		view.Set = true
		view.Key = key
		view.Source = "保管库"
		if !checkout.ValidStripeKey(key) {
			view.Invalid = "当前记录不是有效的 pk_live_ 公钥，结算会被拒绝。"
		}
	}
	catalog := catalogView{}
	if cat, err := checkout.ReadCatalog(s.vault); err != nil {
		catalog.Error = "套餐目录不可用，无法创建付款链接。"
	} else {
		catalog.Available = true
		catalog.Merchant = cat.Merchant
		catalog.Currency = strings.ToUpper(cat.Currency)
		catalog.Plans = cat.Plans
	}
	current, _ := readAnnouncement(s.vault)
	ready, _ := s.paymentsAvailable()
	reply(w, 200, settingsResponse{
		StripeKey:       view,
		Catalog:         catalog,
		PaymentsEnabled: ready,
		Announcement:    current,
		Storage:         s.storageReport(),
		Retention:       s.retention.report(),
	})
}

func (s *server) storageReport() storageReport {
	var report storageReport
	for _, name := range []string{"vault.db", "site.db", "site.db-wal", "site.db-shm"} {
		if info, err := os.Stat(filepath.Join(s.dir, name)); err == nil && info.Size() > 0 {
			report.Files = append(report.Files, storageFile{Name: name, Bytes: info.Size()})
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.dir, &stat); err == nil {
		report.FreeBytes = int64(stat.Bavail) * int64(stat.Bsize)
		report.TotalBytes = int64(stat.Blocks) * int64(stat.Bsize)
	}
	if s.logs != nil {
		report.LogFiles = s.logs.Files()
		for _, f := range report.LogFiles {
			report.LogBytes += f.Bytes
		}
	}
	days := map[string]int{}
	for _, rule := range retentionRules {
		days[rule.Prefix] = rule.Days
	}
	for _, entry := range usagePrefixes {
		usage, err := s.vault.CountPrefixes(entry.Prefix)
		if err != nil {
			continue
		}
		got := usage[entry.Prefix]
		report.Records = append(report.Records, recordUsage{Prefix: entry.Prefix, Label: entry.Label, Records: got.Records, Bytes: got.Bytes, Pruned: days[entry.Prefix] > 0, Days: days[entry.Prefix]})
		report.RecordsBytes += got.Bytes
	}
	return report
}

// saveStripeKey is the manual path that replaces "edit XGIFT_STRIPE_KEY and
// recreate the container". The environment seed only writes when the record is
// absent, so before this existed there was no way to change the key at all.
func (s *server) saveStripeKey(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Key string `json:"key"`
	}
	if !decode(w, r, &q) {
		return
	}
	key := strings.TrimSpace(q.Key)
	if key == "" {
		message(w, 400, "请填写 Stripe 公钥。")
		return
	}
	if !checkout.ValidStripeKey(key) {
		message(w, 400, "这不是有效的 pk_live_ 公钥；请填写 X 结账页使用的 Stripe publishable key。")
		return
	}
	if err := s.vault.Put("stripe-key", []byte(key)); err != nil {
		message(w, 503, "保存失败，请稍后重试。")
		return
	}
	// Record the shape of the key rather than its value: enough to tell which
	// key is active without putting a credential in a downloadable file.
	s.logs.Event(oplog.Entry{Kind: "admin", Level: "info", IP: clientIP(r), Message: "stripe publishable key updated", Extra: map[string]any{"prefix": key[:min(len(key), 12)], "length": len(key)}})
	message(w, 200, "Stripe 公钥已保存，下一次生成链接即生效。")
}

func (s *server) runMaintenance(w http.ResponseWriter, r *http.Request) {
	result := s.maintain()
	if result.Err != "" {
		message(w, 503, "清理未完成："+result.Err)
		return
	}
	reply(w, 200, map[string]any{"result": result, "message": "清理完成。", "storage": s.storageReport()})
}

// retentionState is the janitor's last outcome, shown in the settings panel so
// "is anything actually pruning?" has an answer.
type retentionState struct {
	mu   sync.Mutex
	last retentionResult
}

func (s *retentionState) record(result retentionResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = result
}

func (s *retentionState) report() retentionReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules := make([]retentionRuleView, 0, len(retentionRules))
	for _, rule := range retentionRules {
		rules = append(rules, retentionRuleView{Prefix: rule.Prefix, Label: rule.Label, Days: rule.Days})
	}
	return retentionReport{LastRun: s.last.At, Last: s.last, Rules: rules}
}

type retentionReport struct {
	LastRun int64               `json:"last_run"`
	Last    retentionResult     `json:"last"`
	Rules   []retentionRuleView `json:"rules"`
}

type retentionRuleView struct {
	Prefix string `json:"prefix"`
	Label  string `json:"label"`
	Days   int    `json:"days"`
}

// pruneCutoff is separated so the janitor's window is directly testable.
func pruneCutoff(now time.Time, days int) int64 {
	return now.AddDate(0, 0, -days).Unix()
}
