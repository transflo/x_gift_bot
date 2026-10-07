package site

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"xgift/internal/accounts"
	"xgift/internal/checkout"
	"xgift/internal/oplog"
	"xgift/internal/vault"
)

//go:embed all:web
var webAssets embed.FS

type nonceContextKey struct{}

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)
var codePattern = regexp.MustCompile(`^XG-[A-F0-9]{48}$`)

// healthInterval is how often the pool probes every enabled account's proxy.
const healthInterval = 5 * time.Minute

type server struct {
	turnstileSiteKey string
	turnstileSecret  string
	turnstileHTTP    *http.Client
	db               *sql.DB
	vault            *vault.Vault
	accounts         *accounts.Manager
	logs             *oplog.Log
	origin           string
	adminHash        [32]byte
	payments         bool
	dir              string
	adminPath        string
	lockPath         string
	work             chan struct{}
	checks           chan struct{}
	jobs             sync.WaitGroup
	ctx              context.Context
	limitsMu         sync.Mutex
	limits           map[string]limit
	jobsMu           sync.Mutex
	linkJobs         map[string]bool
	retention        retentionState
}

type limit struct {
	start time.Time
	count int
}

type codeRow struct {
	Copyable        bool   `json:"copyable"`
	Folder          string `json:"folder"`
	ID              string `json:"id"`
	Hint            string `json:"hint"`
	Batch           string `json:"batch"`
	Months          int    `json:"months"`
	Status          string `json:"status"`
	Progress        int    `json:"progress"`
	RecipientID     string `json:"-"`
	Username        string `json:"username"`
	Message         string `json:"message"`
	StripeURL       string `json:"checkout_url,omitempty"`
	StripeSession   string `json:"-"`
	LinkCreated     int64  `json:"link_created,omitempty"`
	LinkRegenerated bool   `json:"link_regenerated,omitempty"`
	LinkState       string `json:"link_state,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	Created         int64  `json:"created"`
	Updated         int64  `json:"updated"`
}

const codeColumns = "id,hint,batch,months,status,username,message,created,updated,progress,COALESCE(recipient_id,''),COALESCE(stripe_url,''),COALESCE(stripe_session,''),link_created,link_regenerated,COALESCE(account_id,''),COALESCE(link_state,'')"

const codeColumnsWithFolder = codeColumns + ",COALESCE(folder_id,''),copyable"

func scanCode(row interface{ Scan(...any) error }, c *codeRow) error {
	var regenerated int
	if err := row.Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.RecipientID, &c.StripeURL, &c.StripeSession, &c.LinkCreated, &regenerated, &c.AccountID, &c.LinkState); err != nil {
		return err
	}
	c.LinkRegenerated = regenerated != 0
	return nil
}

func scanCodeWithFolder(row interface{ Scan(...any) error }, c *codeRow) error {
	var regenerated, copyable int
	if err := row.Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.RecipientID, &c.StripeURL, &c.StripeSession, &c.LinkCreated, &regenerated, &c.AccountID, &c.LinkState, &c.Folder, &copyable); err != nil {
		return err
	}
	c.LinkRegenerated = regenerated != 0
	c.Copyable = copyable != 0
	return nil
}

// linkStates are the persisted payment-link outcomes the admin panel reports.
// "expired" is the one state derived at read time: a link can age out without
// anything happening, so no write would otherwise record it.
const (
	linkNone     = ""
	linkWaiting  = "waiting"
	linkPaid     = "paid"
	linkDeclined = "declined"
	linkExpired  = "expired"
)

// decorate fills in the derived link fields for one order.
func decorate(c *codeRow) {
	switch {
	case c.Status == "succeeded":
		c.LinkState = linkPaid
	case c.StripeURL == "":
		c.LinkState = linkNone
	case c.LinkState == linkWaiting && linkExpires(c) < time.Now().Unix():
		c.LinkState = linkExpired
	}
}

// linkExpires is the moment the published payment window closes.
func linkExpires(c *codeRow) int64 {
	if c.LinkCreated == 0 {
		return 0
	}
	return c.LinkCreated + int64(checkout.PublicLinkTTL/time.Second)
}

func Run(ctx context.Context) error {
	ctx, cancelService := context.WithCancel(ctx)
	defer cancelService()
	origin := os.Getenv("XGIFT_ORIGIN")
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("XGIFT_ORIGIN must be an HTTPS origin")
	}
	dir := os.Getenv("XGIFT_DATA_DIR")
	if dir == "" {
		return errors.New("XGIFT_DATA_DIR is required")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	instance, err := os.OpenFile(filepath.Join(dir, "site.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err = syscall.Flock(int(instance.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another site instance is using this data directory")
	}
	admin, err := adminPassword(dir)
	if err != nil {
		return err
	}
	if len(admin) < 32 {
		return errors.New("admin password must contain at least 32 characters")
	}
	slug, err := adminPath(dir)
	if err != nil {
		return err
	}
	s := &server{origin: origin, adminHash: sha256.Sum256(admin), payments: os.Getenv("XGIFT_PAYMENTS_ENABLED") == "true", dir: dir, adminPath: slug, lockPath: filepath.Join(dir, "checkout.lock"), work: make(chan struct{}, 4), checks: make(chan struct{}, 4), ctx: ctx, limits: map[string]limit{}, linkJobs: map[string]bool{}}
	clear(admin)
	if err = s.configureTurnstile(); err != nil {
		return err
	}
	v, err := openVault(dir)
	if err != nil {
		return err
	}
	s.vault = v
	defer v.Close()
	if err = seedDefaults(v); err != nil {
		return err
	}
	// From here on the process's own output is also written to the data
	// directory, so the admin panel can show and download it.
	logs, err := oplog.Open(filepath.Join(dir, "logs"))
	if err != nil {
		return err
	}
	s.logs = logs
	defer logs.Close()
	log.SetOutput(io.MultiWriter(os.Stderr, logs.Writer()))
	s.accounts = accounts.NewManager(ctx)
	defer s.accounts.Close()
	// Probe the pool in the background so a proxy that dies between checkouts is
	// noticed before a customer hits it.
	go s.accounts.Watch(ctx, v, healthInterval)
	if _, err = accounts.Load(v); err != nil {
		// Boot succeeds so the admin UI can be used to configure the pool. All
		// creation paths fail closed until accounts are valid.
		log.Printf("account pool is not ready: %v", err)
	}
	dbpath := filepath.Join(dir, "site.db")
	f, err := os.OpenFile(dbpath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	if err = os.Chmod(dbpath, 0600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", dbpath+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on")
	if err != nil {
		return err
	}
	s.db = db
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = s.migrate(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /livez", s.live)
	mux.HandleFunc("GET /api/security", s.securityConfig)
	mux.HandleFunc("GET /api/announcement", s.announcement)
	mux.HandleFunc("POST /api/redeem", s.human("redeem", s.redeem))
	mux.HandleFunc("POST /api/redeem/regenerate", s.human("redeem", s.regenerate))
	mux.HandleFunc("POST /api/status", s.status)
	mux.HandleFunc("POST /api/check", s.human("check", s.check))
	// The admin UI and its API hang off a per-install random segment, so a scan
	// of /admin finds nothing. Both share the prefix; the admin bundle reads it
	// back from location.pathname.
	panel := "/" + s.adminPath
	api := panel + "/api/admin/"
	// The admin page itself is a static shell and carries no data, so it is not
	// behind Basic auth — that would stack the browser's native prompt on top of
	// the panel's own login form. The random segment keeps it out of scanners and
	// every API call it makes is authenticated.
	mux.HandleFunc("GET "+panel, s.staticPage("admin/index.html"))
	mux.HandleFunc("GET "+panel+"/{$}", s.staticPage("admin/index.html"))
	mux.HandleFunc("GET "+api+"codes", s.admin(s.list))
	mux.HandleFunc("POST "+api+"lookup", s.admin(s.lookup))
	mux.HandleFunc("GET "+api+"stats", s.admin(s.stats))
	mux.HandleFunc("GET "+api+"customer", s.admin(s.customerOrder))
	mux.HandleFunc("GET "+api+"accounts", s.admin(s.listAccounts))
	mux.HandleFunc("GET "+api+"vault", s.admin(s.vaultBackup))
	mux.HandleFunc("GET "+api+"settings", s.admin(s.readSettings))
	mux.HandleFunc("POST "+api+"settings/stripe-key", s.admin(s.saveStripeKey))
	mux.HandleFunc("POST "+api+"settings/announcement", s.admin(s.saveAnnouncement))
	mux.HandleFunc("POST "+api+"settings/maintenance", s.admin(s.runMaintenance))
	mux.HandleFunc("GET "+api+"logs", s.admin(s.readLogs))
	mux.HandleFunc("GET "+api+"logs/download", s.admin(s.downloadLog))
	mux.HandleFunc("POST "+api+"accounts", s.admin(s.saveAccount))
	mux.HandleFunc("POST "+api+"accounts/delete", s.admin(s.deleteAccount))
	mux.HandleFunc("POST "+api+"accounts/toggle", s.admin(s.toggleAccount))
	mux.HandleFunc("POST "+api+"accounts/test", s.admin(s.testAccount))
	mux.HandleFunc("GET "+api+"manual-link/plans", s.admin(s.manualLinkPlans))
	mux.HandleFunc("POST "+api+"manual-link", s.admin(s.manualLink))
	mux.HandleFunc("POST "+api+"codes", s.admin(s.generate))
	mux.HandleFunc("POST "+api+"revoke", s.admin(s.revoke))
	mux.HandleFunc("POST "+api+"folders", s.admin(s.createFolder))
	mux.HandleFunc("POST "+api+"folders/rename", s.admin(s.renameFolder))
	mux.HandleFunc("POST "+api+"folders/delete", s.admin(s.deleteFolder))
	mux.HandleFunc("POST "+api+"codes/move", s.admin(s.moveCodes))
	mux.HandleFunc("POST "+api+"codes/copy", s.admin(s.copyCode))
	mux.HandleFunc("/", s.staticFiles)
	addr, err := listenAddress()
	if err != nil {
		return err
	}
	h := &http.Server{Addr: addr, Handler: s.middleware(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- h.ListenAndServe() }()
	log.Printf("xgift-web listening on %s; payments enabled=%t", addr, s.payments)
	log.Printf("管理后台：%s/%s/（每次安装随机生成，请连同密码一起保存）", strings.TrimSuffix(origin, "/"), s.adminPath)
	s.jobs.Add(1)
	go func() { defer s.jobs.Done(); s.reconcileLoop() }()
	s.jobs.Add(1)
	go func() { defer s.jobs.Done(); s.maintenanceLoop() }()
	select {
	case err = <-done:
	case <-ctx.Done():
	}
	cancelService()
	shutdown, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	if e := h.Shutdown(shutdown); e != nil {
		h.Close()
	}
	s.jobs.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// migrate creates the schema additively so existing redemption codes and
// orders survive every upgrade.
func (s *server) migrate() error {
	db := s.db
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS codes (
 id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL, batch TEXT NOT NULL,
 months INTEGER NOT NULL CHECK(months IN (3,6)),
 status TEXT NOT NULL CHECK(status IN ('active','processing','succeeded','review','revoked')),
 username TEXT NOT NULL DEFAULT '', recipient_id TEXT UNIQUE,
 message TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL
 ); CREATE INDEX IF NOT EXISTS codes_created ON codes(created);`)
	if err != nil {
		return err
	}
	columns := []struct{ name, ddl string }{
		{"progress", "ALTER TABLE codes ADD COLUMN progress INTEGER NOT NULL DEFAULT 0"},
		{"stripe_url", "ALTER TABLE codes ADD COLUMN stripe_url TEXT NOT NULL DEFAULT ''"},
		{"stripe_session", "ALTER TABLE codes ADD COLUMN stripe_session TEXT NOT NULL DEFAULT ''"},
		{"link_created", "ALTER TABLE codes ADD COLUMN link_created INTEGER NOT NULL DEFAULT 0"},
		{"link_regenerated", "ALTER TABLE codes ADD COLUMN link_regenerated INTEGER NOT NULL DEFAULT 0"},
		{"link_checked", "ALTER TABLE codes ADD COLUMN link_checked INTEGER NOT NULL DEFAULT 0"},
		{"account_id", "ALTER TABLE codes ADD COLUMN account_id TEXT NOT NULL DEFAULT ''"},
		{"link_state", "ALTER TABLE codes ADD COLUMN link_state TEXT NOT NULL DEFAULT ''"},
	}
	existing, err := tableColumns(db, "codes")
	if err != nil {
		return err
	}
	for _, c := range columns {
		if existing[c.name] {
			continue
		}
		if _, err = db.Exec(c.ddl); err != nil {
			return err
		}
	}
	if err = migrateFolders(db); err != nil {
		return err
	}
	return migrateBatches(db)
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, required, primary int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &required, &defaultValue, &primary); err != nil {
			return nil, err
		}
		found[name] = true
	}
	return found, rows.Err()
}

func privateFile(path string) ([]byte, error) {
	i, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return nil, errors.New("secret file must be regular and owner-only")
	}
	return os.ReadFile(path)
}

func token(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

func hash(code string) string { b := sha256.Sum256([]byte(code)); return hex.EncodeToString(b[:]) }

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func message(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]any{"message": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		message(w, 415, "请使用 JSON 请求。")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		message(w, 400, "请求格式不正确。")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		message(w, 400, "请求格式不正确。")
		return false
	}
	return true
}

func (s *server) allow(key string, max int) bool {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	now := time.Now()
	if len(s.limits) > 10000 {
		for k, v := range s.limits {
			if now.Sub(v.start) > time.Minute {
				delete(s.limits, k)
			}
		}
		if len(s.limits) > 10000 {
			return false
		}
	}
	l := s.limits[key]
	if now.Sub(l.start) > time.Minute {
		l = limit{start: now}
	}
	l.count++
	s.limits[key] = l
	return l.count <= max
}

// clientIP resolves the visitor's real address. The service is expected to sit
// behind a trusted reverse proxy on loopback, so the proxy's header is
// authoritative; the socket peer is only a fallback, and inside a container
// that peer is always the proxy's private address — which is why the documented
// Caddy and Nginx snippets set these headers.
func clientIP(r *http.Request) string {
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		for _, part := range strings.Split(forwarded, ",") {
			if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

// unlogged keeps high-frequency noise out of the file. Health probes run every
// 30 seconds and build assets are one request per file per deploy; recording
// them would bury the requests an operator opened the panel to read. Next emits
// its assets and route-prefetch payloads under several spellings, so the check
// is by prefix rather than by exact path.
func unlogged(path string) bool {
	if path == "/livez" || path == "/healthz" || path == "/favicon.ico" {
		return true
	}
	return strings.HasPrefix(path, "/_next") || strings.HasPrefix(path, "/__next")
}

// statusWriter records the response code so the access log can report it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (s *server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		nonce := token(16)
		r = r.WithContext(context.WithValue(r.Context(), nonceContextKey{}, nonce))
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'nonce-"+nonce+"' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; style-src 'self' 'nonce-"+nonce+"'; style-src-attr 'unsafe-inline'; connect-src 'self' https://challenges.cloudflare.com; worker-src 'self' blob:; img-src 'self' data:; media-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		ip := clientIP(r)
		started := time.Now()
		recorded := &statusWriter{ResponseWriter: w}
		if !unlogged(r.URL.Path) {
			defer func() { s.logRequest(r, recorded.status, ip, time.Since(started)) }()
		}
		if r.Method == http.MethodPost && r.Header.Get("Origin") != s.origin {
			message(recorded, 403, "请求来源不正确，请从本站页面重试。")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/admin") {
			max := 60
			bucket := "api:"
			if r.URL.Path == "/api/redeem" {
				max = 8
				bucket = "redeem:"
			} else if r.URL.Path == "/api/redeem/regenerate" {
				max = 4
				bucket = "regenerate:"
			} else if r.URL.Path == "/api/check" {
				max = 8
				bucket = "check:"
			} else if r.URL.Path == "/api/status" {
				max = 120
				bucket = "status:"
			}
			if !s.allow(bucket+ip, max) {
				recorded.Header().Set("Retry-After", "60")
				message(recorded, 429, "操作太频繁，请稍后重试。")
				return
			}
		}
		next.ServeHTTP(recorded, r)
	})
}

// logRequest writes one access entry. The query string is dropped rather than
// recorded: a redemption code travels in a POST body, but keeping URLs free of
// parameters means the downloaded file can be shared without auditing it first.
func (s *server) logRequest(r *http.Request, status int, ip string, elapsed time.Duration) {
	if s.logs == nil || status == 0 {
		return
	}
	userAgent := r.Header.Get("User-Agent")
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	s.logs.Event(oplog.Entry{
		Kind:    "http",
		Level:   accessLevel(status),
		IP:      ip,
		Method:  r.Method,
		Path:    r.URL.Path,
		Status:  status,
		MS:      elapsed.Milliseconds(),
		UA:      userAgent,
		Referer: r.Header.Get("Referer"),
		Message: r.URL.Path,
		Extra:   map[string]any{"query": r.URL.RawQuery != ""},
	})
}

func accessLevel(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "warn"
	default:
		return "info"
	}
}

// admin guards the admin API. Any username is accepted — the panel is
// single-operator and the password is the only credential — so the browser's
// native Basic prompt and the in-app form both work.
func (s *server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		sum := sha256.Sum256([]byte(password))
		if !ok || subtle.ConstantTimeCompare(sum[:], s.adminHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="XGift Admin", charset="UTF-8"`)
			message(w, 401, "需要管理员登录。")
			return
		}
		next(w, r)
	}
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if s.db.PingContext(r.Context()) != nil {
		reply(w, 503, map[string]any{"ok": false})
		return
	}
	ready, err := s.paymentsAvailable()
	if err != nil {
		reply(w, 503, map[string]any{"ok": false, "payments_enabled": false})
		return
	}
	reply(w, 200, map[string]any{"ok": true, "payments_enabled": ready})
}

// live reports only that the process is up and its database answers. A fresh
// install has no account pool yet, which makes it unready but perfectly alive —
// reporting that as unhealthy would make `docker compose up -d` look broken
// until the operator adds their first account.
func (s *server) live(w http.ResponseWriter, r *http.Request) {
	if s.db.PingContext(r.Context()) != nil {
		reply(w, 503, map[string]any{"ok": false})
		return
	}
	reply(w, 200, map[string]any{"ok": true})
}

func (s *server) find(code string) (codeRow, error) {
	var c codeRow
	e := scanCode(s.db.QueryRow("SELECT "+codeColumns+" FROM codes WHERE hash=?", hash(code)), &c)
	return c, e
}

func readInput(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	var q struct {
		Code     string `json:"code"`
		Username string `json:"username"`
	}
	if !decode(w, r, &q) {
		return "", "", false
	}
	q.Code = strings.ToUpper(strings.TrimSpace(q.Code))
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !codePattern.MatchString(q.Code) || !usernamePattern.MatchString(q.Username) {
		message(w, 400, "请填写完整兑换码和正确的 X 用户名（不是显示名称）。")
		return "", "", false
	}
	return q.Code, q.Username, true
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	code, user, ok := readInput(w, r)
	if !ok {
		return
	}
	c, err := s.find(code)
	if err != nil || c.Username != "" && c.Username != user {
		message(w, 404, "兑换码或用户名不匹配。")
		return
	}
	if c.Status == "processing" && c.RecipientID != "" {
		s.scheduleLinkCheck(c)
	}
	replyRedemption(w, 200, c)
}

func replyRedemption(w http.ResponseWriter, status int, c codeRow) {
	msg := c.Message
	if msg == "" {
		switch c.Status {
		case "active":
			msg = "兑换码尚未使用。填写 X 用户名后点击「开始兑换」，系统会立即创建付款链接。"
		default:
			msg = "正在处理，请保持本页面打开。"
		}
	}
	result := map[string]any{
		"status":           c.Status,
		"months":           c.Months,
		"message":          msg,
		"progress":         c.Progress,
		"username":         c.Username,
		"checkout_url":     c.StripeURL,
		"link_ready":       c.StripeURL != "" && c.LinkCreated > 0,
		"can_regenerate":   canRegenerate(c),
		"link_regenerated": c.LinkRegenerated,
	}
	if c.LinkCreated > 0 {
		result["expires_at"] = c.LinkCreated + int64(checkout.PublicLinkTTL/time.Second)
	}
	reply(w, status, result)
}

func canRegenerate(c codeRow) bool {
	return c.Status == "processing" && c.LinkCreated > 0 && !c.LinkRegenerated
}

// check is a read-only eligibility probe: no code lookup, no checkout, no
// writes. It stays available while payments are paused.
func (s *server) check(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Username string `json:"username"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !usernamePattern.MatchString(q.Username) {
		message(w, 400, "请填写正确的 X 用户名（不是显示名称）。")
		return
	}
	select {
	case s.checks <- struct{}{}:
		defer func() { <-s.checks }()
	default:
		message(w, 503, "当前检测人数较多，请稍后重试检测。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	_, err := checkout.Eligibility(ctx, s.vault, s.accounts, q.Username)
	if err != nil {
		switch {
		case errors.Is(err, checkout.ErrNotEligible):
			reply(w, 200, map[string]any{"eligible": false, "message": "X 当前不允许向这个账号赠送 Premium。"})
		case errors.Is(err, checkout.ErrUserNotFound):
			reply(w, 200, map[string]any{"eligible": false, "message": "未能找到这个 X 账号，请检查用户名。"})
		default:
			message(w, 503, "暂时无法向 X 核实赠送资格，请稍后重试检测。")
		}
		return
	}
	reply(w, 200, map[string]any{"eligible": true, "message": "该账号当前可以接收赠送。"})
}

func (s *server) generate(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Folder string `json:"folder"`
		Months int    `json:"months"`
		Count  int    `json:"count"`
		Batch  string `json:"batch"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Batch = strings.TrimSpace(q.Batch)
	if (q.Months != 3 && q.Months != 6) || q.Count < 1 || q.Count > 500 || len(q.Batch) > 120 || (q.Folder != "" && !folderIDPattern.MatchString(q.Folder)) {
		message(w, 400, "请选择 3 或 6 个月，数量 1–500，批次名称不超过 120 字节。")
		return
	}
	if q.Batch == "" {
		q.Batch = time.Now().UTC().Format("20060102-150405") + "-" + token(3)
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		message(w, 503, "暂时无法生成兑换码。")
		return
	}
	defer tx.Rollback()
	folder, e := ensureBatch(tx, q.Batch)
	if e != nil {
		message(w, 503, "无法保存批次。")
		return
	}
	if e = tx.QueryRow("SELECT name FROM folders WHERE id=?", folder).Scan(&q.Batch); e != nil {
		message(w, 503, "无法读取批次。")
		return
	}
	codes := make([]string, 0, q.Count)
	now := time.Now().Unix()
	for i := 0; i < q.Count; i++ {
		code := "XG-" + strings.ToUpper(token(24))
		id := token(16)
		// Persist the encrypted content first; an interrupted transaction can
		// only leave an unreachable vault record, never an active code without
		// its encrypted value.
		if e = s.vault.Put("redemption:"+id, []byte(code)); e != nil {
			message(w, 503, "无法加密保存兑换码，尚未生成本批。")
			return
		}
		_, e = tx.Exec("INSERT INTO codes(id,hash,hint,batch,months,status,created,updated,folder_id,copyable) VALUES(?,?,?,?,?,'active',?,?,?,1)", id, hash(code), code[len(code)-8:], q.Batch, q.Months, now, now, folder)
		if e != nil {
			message(w, 503, "生成失败，没有保存本批兑换码。")
			return
		}
		codes = append(codes, code)
	}
	if e = tx.Commit(); e != nil {
		message(w, 503, "保存结果不确定，请在后台核实批次后再操作。")
		return
	}
	reply(w, 201, map[string]any{"codes": codes, "batch": q.Batch, "months": q.Months, "folder": folder})
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &q) {
		return
	}
	res, e := s.db.Exec("UPDATE codes SET status='revoked',message='兑换码已停用',updated=? WHERE id=? AND status IN ('active','processing')", time.Now().Unix(), q.ID)
	if e != nil {
		message(w, 503, "停用失败。")
		return
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		message(w, 409, "只能停用尚未完成的兑换码。")
		return
	}
	message(w, 200, "兑换码已停用。")
}

func (s *server) manualLinkPlans(w http.ResponseWriter, r *http.Request) {
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		message(w, 503, "套餐配置暂不可用。")
		return
	}
	type plan struct {
		Months   int    `json:"months"`
		Amount   int    `json:"amount"`
		Currency string `json:"currency"`
	}
	plans := make([]plan, 0, len(cat.Plans))
	for _, p := range cat.Plans {
		plans = append(plans, plan{p.Months, p.Amount, strings.ToUpper(cat.Currency)})
	}
	reply(w, 200, map[string]any{"plans": plans})
}

// staticPage returns one embedded HTML page with CSP nonce placeholders
// substituted per request.
func (s *server) staticPage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := webAssets.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, r, b)
	}
}

func writeHTML(w http.ResponseWriter, r *http.Request, b []byte) {
	nonce, _ := r.Context().Value(nonceContextKey{}).(string)
	b = []byte(strings.ReplaceAll(string(b), "__XGIFT_NONCE__", nonce))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// staticFiles serves the Next.js static export. Hashed build assets are
// immutable; HTML receives a fresh CSP nonce on every request.
func (s *server) staticFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		message(w, http.StatusMethodNotAllowed, "请求方法不受支持。")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || strings.HasSuffix(r.URL.Path, "/") {
		name = path.Join(name, "index.html")
	} else if !strings.Contains(path.Base(name), ".") {
		name = path.Join(name, "index.html")
	}
	b, err := s.asset(name)
	if err != nil {
		if fallback, ferr := webAssets.ReadFile("web/404.html"); ferr == nil {
			w.WriteHeader(http.StatusNotFound)
			writeHTML(w, r, fallback)
			return
		}
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".html") {
		writeHTML(w, r, b)
		return
	}
	if kind := mime.TypeByExtension(path.Ext(name)); kind != "" {
		w.Header().Set("Content-Type", kind)
	}
	if strings.HasPrefix(name, "_next/static/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Write(b)
}

// asset reads one exported file. The admin bundle is deliberately unreachable
// here: it is served only at the per-install random segment, and handing out the
// exported admin/index.html would leave the guessable /admin/ working — exactly
// the path the random segment exists to remove.
func (s *server) asset(name string) ([]byte, error) {
	if name == "admin/index.html" {
		return nil, os.ErrNotExist
	}
	return webAssets.ReadFile("web/" + name)
}

// listenAddress validates XGIFT_LISTEN separately so startup errors are clear.
func listenAddress() (string, error) {
	addr := os.Getenv("XGIFT_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("XGIFT_LISTEN must be an IP:port pair")
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("XGIFT_LISTEN must use an IP address")
	}
	if !net.ParseIP(host).IsLoopback() && os.Getenv("XGIFT_ALLOW_PUBLIC_LISTEN") != "true" {
		return "", fmt.Errorf("XGIFT_LISTEN must be loopback unless XGIFT_ALLOW_PUBLIC_LISTEN=true")
	}
	return addr, nil
}
