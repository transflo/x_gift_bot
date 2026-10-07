package site

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"xgift/internal/accounts"
	"xgift/internal/checkout"
	"xgift/internal/oplog"
)

func (s *server) paymentsAvailable() (bool, error) {
	if !s.payments {
		return false, nil
	}
	list, err := accounts.Load(s.vault)
	if err != nil {
		return false, err
	}
	if len(accounts.Enabled(list)) == 0 {
		return false, errors.New("no enabled X account is configured")
	}
	return true, nil
}

// redeem binds an unused code to the submitted username and starts link
// generation in the background. The browser learns the result by polling
// /api/status, so a page reload never loses the flow.
func (s *server) redeem(w http.ResponseWriter, r *http.Request) {
	ready, availabilityErr := s.paymentsAvailable()
	if !ready || availabilityErr != nil {
		reply(w, http.StatusServiceUnavailable, map[string]any{"status": "paused", "message": "充值暂时暂停，恢复时间待定。请保留兑换码，已有订单可继续查询进度。"})
		return
	}
	code, user, ok := readInput(w, r)
	if !ok {
		return
	}
	c, err := s.find(code)
	if errors.Is(err, sql.ErrNoRows) {
		message(w, 404, "兑换码不存在，请检查后重试。")
		return
	}
	if err != nil {
		message(w, 503, "服务暂时不可用，兑换码未使用。")
		return
	}
	if c.Username != "" && c.Username != user {
		message(w, 404, "兑换码或用户名不匹配。")
		return
	}
	switch c.Status {
	case "revoked":
		message(w, 409, "兑换码已停用，请联系提供方。")
		return
	case "succeeded":
		replyRedemption(w, 200, c)
		return
	case "review":
		replyRedemption(w, 200, c)
		return
	case "active":
		result, err := s.db.Exec("UPDATE codes SET status='processing',username=?,progress=10,message=?,updated=? WHERE id=? AND status='active'", user, "正在核对账号与赠送资格…", time.Now().Unix(), c.ID)
		if err != nil {
			message(w, 503, "无法开始处理订单，请刷新后查询兑换状态。")
			return
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			fresh, findErr := s.find(code)
			if findErr == nil {
				replyRedemption(w, 202, fresh)
				return
			}
			message(w, 409, "兑换码状态已改变，请刷新后查询。")
			return
		}
		c.Status, c.Username, c.Progress, c.Message = "processing", user, 10, "正在核对账号与赠送资格…"
	case "processing":
		// Already bound or waiting; the job below resumes or continues it.
	default:
		message(w, 409, "兑换码状态异常，请联系提供方。")
		return
	}
	s.startLinkJob(c.ID, user, false)
	replyRedemption(w, http.StatusAccepted, c)
}

// regenerate publishes one replacement link per code. It is the only manual
// recovery action available to a customer.
func (s *server) regenerate(w http.ResponseWriter, r *http.Request) {
	ready, availabilityErr := s.paymentsAvailable()
	if !ready || availabilityErr != nil {
		reply(w, http.StatusServiceUnavailable, map[string]any{"status": "paused", "message": "充值暂时暂停，恢复时间待定。"})
		return
	}
	code, user, ok := readInput(w, r)
	if !ok {
		return
	}
	c, err := s.find(code)
	if err != nil || c.Username != user {
		message(w, 404, "兑换码或用户名不匹配。")
		return
	}
	if c.Status == "succeeded" {
		message(w, 409, "本次兑换已经完成，无需重新生成。")
		return
	}
	if c.Status != "processing" || c.StripeURL == "" {
		message(w, 409, "当前没有可重新生成的付款链接。")
		return
	}
	if c.LinkRegenerated {
		message(w, 409, "每个兑换码只能重新生成一次付款链接。")
		return
	}
	result, err := s.db.Exec("UPDATE codes SET link_regenerated=1,message=?,updated=? WHERE id=? AND link_regenerated=0 AND status='processing'", "正在重新生成付款链接…", time.Now().Unix(), c.ID)
	if err != nil {
		message(w, 503, "暂时无法重新生成，请稍后重试。")
		return
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		message(w, 409, "刚刚已经重新生成过，请刷新页面查看最新链接。")
		return
	}
	c.LinkRegenerated = true
	c.Message = "正在重新生成付款链接…"
	s.startLinkJob(c.ID, user, true)
	replyRedemption(w, http.StatusAccepted, c)
}

func (s *server) startLinkJob(codeID, user string, replace bool) {
	s.jobsMu.Lock()
	if s.linkJobs[codeID] {
		s.jobsMu.Unlock()
		return
	}
	s.linkJobs[codeID] = true
	s.jobsMu.Unlock()
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer func() {
			s.jobsMu.Lock()
			delete(s.linkJobs, codeID)
			s.jobsMu.Unlock()
		}()
		s.runLinkJob(codeID, user, replace)
	}()
}

func (s *server) updateProcessing(codeID string, progress int, msg string) {
	if _, err := s.db.Exec("UPDATE codes SET progress=MAX(progress,?),message=?,updated=? WHERE id=? AND status='processing'", progress, msg, time.Now().Unix(), codeID); err != nil {
		log.Printf("order %s progress could not be saved", codeID)
	}
}

// runLinkJob is the async worker that binds eligibility and publishes the
// Stripe link for one code. It serializes on checkout.lock exactly like the
// CLI so an operator command can never race a web creation.
func (s *server) runLinkJob(codeID, user string, replace bool) {
	ctx, cancel := context.WithTimeout(s.ctx, 4*time.Minute)
	defer cancel()
	select {
	case s.work <- struct{}{}:
		defer func() { <-s.work }()
	case <-ctx.Done():
		return
	}
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.updateProcessing(codeID, 10, "订单服务暂时不可用，请稍后重试。")
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		s.updateProcessing(codeID, 10, "系统中正在处理其他订单，请稍后重试。")
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	var c codeRow
	if err = scanCode(s.db.QueryRow("SELECT "+codeColumns+" FROM codes WHERE id=? AND status='processing'", codeID), &c); err != nil {
		return
	}
	recipient := c.RecipientID
	if !replace && recipient == "" {
		probe, probeErr := checkout.Eligibility(ctx, s.vault, s.accounts, user)
		if probeErr != nil {
			s.redeemEligibilityFailure(codeID, probeErr)
			return
		}
		recipient = probe
		if _, err = s.db.Exec("UPDATE codes SET recipient_id=?,updated=? WHERE id=? AND status='processing' AND (recipient_id IS NULL OR recipient_id='')", recipient, time.Now().Unix(), codeID); err != nil {
			s.updateProcessing(codeID, 10, "无法保存订单绑定，请稍后重试。")
			return
		}
	}
	if recipient == "" {
		s.updateProcessing(codeID, 10, "无法确认接收账号，请稍后重试。")
		return
	}
	ctx = checkout.WithProgress(ctx, func(percent int, msg string) { s.updateProcessing(codeID, percent, msg) })

	for {
		if ctx.Err() != nil {
			s.updateProcessing(codeID, 30, "生成超时，请稍后重试。")
			return
		}
		record, createErr := checkout.CreateLinkForRecipient(ctx, s.vault, s.accounts, checkout.LinkRequest{
			User:      user,
			Recipient: recipient,
			Months:    c.Months,
			Replace:   replace,
			AccountID: c.AccountID,
		})
		if createErr == nil && record != nil {
			s.publishLink(codeID, record)
			return
		}
		if errors.Is(createErr, checkout.ErrCheckoutRateLimited) {
			wait := 10 * time.Second
			var waitErr *checkout.CheckoutWaitError
			if errors.As(createErr, &waitErr) && waitErr.Wait > 0 {
				wait = waitErr.Wait
			}
			if wait > 15*time.Second {
				wait = 15 * time.Second
			}
			s.updateProcessing(codeID, 30, fmt.Sprintf("正在等待通道空闲，约 %d 秒后继续…", int((wait+time.Second-1)/time.Second)))
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		s.redeemLinkFailure(codeID, recipient, replace, createErr)
		return
	}
}

func (s *server) publishLink(codeID string, record *checkout.Record) {
	message := "付款链接已生成，请在有效期内完成付款；付款完成后本页面会自动更新。"
	if _, err := s.db.Exec("UPDATE codes SET stripe_url=?,stripe_session=?,link_created=?,account_id=?,link_state=?,progress=80,message=?,updated=?,link_checked=0 WHERE id=? AND status='processing'",
		record.URL, record.SessionID, record.Created, record.AccountID, linkWaiting, message, time.Now().Unix(), codeID); err != nil {
		log.Printf("order %s link could not be saved", codeID)
	}
	s.logs.Event(oplog.Entry{Kind: "order", Level: "info", Message: "payment link published", Extra: map[string]any{"order": codeID, "account": record.AccountID, "months": record.Months, "session": record.SessionID}})
}

func (s *server) redeemEligibilityFailure(codeID string, err error) {
	msg := "暂时无法向 X 核实账号资格，请稍后重试。"
	if errors.Is(err, checkout.ErrNotEligible) {
		msg = "X 当前不允许向这个账号赠送 Premium。兑换码未使用，可换一个符合条件的账号。"
	} else if errors.Is(err, checkout.ErrUserNotFound) {
		msg = "未能找到这个 X 账号，请检查用户名。兑换码未使用。"
	} else if errors.Is(err, accounts.ErrNoEnabledAccount) {
		msg = "充值通道维护中，请稍后重试。兑换码未使用。"
	}
	// The code was bound before the check; release it so another account can
	// still be used with the same code.
	if _, dbErr := s.db.Exec("UPDATE codes SET status='active',username='',recipient_id='',progress=0,message=?,updated=? WHERE id=? AND status='processing' AND (recipient_id IS NULL OR recipient_id='')", msg, time.Now().Unix(), codeID); dbErr != nil {
		log.Printf("order %s could not be released after an eligibility failure", codeID)
	}
}

func (s *server) redeemLinkFailure(codeID, recipient string, replace bool, err error) {
	msg := "暂时无法生成付款链接，请稍后重试；重复请求会优先检查已有订单。"
	switch {
	case errors.Is(err, checkout.ErrPublicPaymentInProgress):
		msg = "该订单正在付款或银行验证中，请先完成当前付款。"
	case errors.Is(err, checkout.ErrPublicPaymentDeclined):
		msg = "上一笔付款被支付机构拒绝，请点击「重新生成链接」后再付款。"
	case errors.Is(err, checkout.ErrVerifyUnpaid):
		msg = "原付款链接已失效，请重新生成链接。"
	case errors.Is(err, checkout.ErrPublicLinkPending):
		msg = "暂未取得付款链接，系统没有提交付款。请稍后重试。"
	case errors.Is(err, checkout.ErrNotEligible):
		msg = "X 当前不允许该账号接收 Premium 赠送，兑换码未使用。"
	case errors.Is(err, checkout.ErrUserNotFound):
		msg = "未找到绑定的 X 账号，兑换码未使用。"
	case errors.Is(err, checkout.ErrPublicLinkConflict), errors.Is(err, checkout.ErrSessionMismatch):
		msg = "该账号已有其他订单记录，请通过查询核实原订单或联系管理员。"
	case errors.Is(err, checkout.ErrNoAccount):
		msg = "充值通道维护中，请稍后重试。"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		msg = "生成超时，请稍后重试。"
	}
	status := "processing"
	if errors.Is(err, checkout.ErrSessionMismatch) {
		status = "review"
	}
	// A failure that ends the payment window is worth recording as a link state
	// too, so the admin list can show "declined" without parsing the message.
	state := linkNone
	switch {
	case errors.Is(err, checkout.ErrPublicPaymentDeclined):
		state = linkDeclined
	case errors.Is(err, checkout.ErrVerifyUnpaid):
		state = linkExpired
	}
	if state == linkNone {
		if _, dbErr := s.db.Exec("UPDATE codes SET status=?,message=?,updated=? WHERE id=? AND status='processing'", status, msg, time.Now().Unix(), codeID); dbErr != nil {
			log.Printf("order %s failure state could not be saved", codeID)
		}
	} else if _, dbErr := s.db.Exec("UPDATE codes SET status=?,link_state=?,message=?,updated=? WHERE id=? AND status='processing'", status, state, msg, time.Now().Unix(), codeID); dbErr != nil {
		log.Printf("order %s failure state could not be saved", codeID)
	}
	log.Printf("order %s link generation failed: %s", codeID, manualLinkFailureReason(err))
	s.logs.Event(oplog.Entry{Kind: "order", Level: "error", Message: "payment link generation failed", Extra: map[string]any{"order": codeID, "recipient": recipient, "reason": manualLinkFailureReason(err), "replace": replace}})
}

// scheduleLinkCheck asynchronously refreshes one pending link at most once
// every 20 seconds.
func (s *server) scheduleLinkCheck(c codeRow) {
	if c.StripeSession == "" || c.RecipientID == "" {
		return
	}
	now := time.Now().Unix()
	result, err := s.db.Exec("UPDATE codes SET link_checked=? WHERE id=? AND link_checked<?", now, c.ID, now-20)
	if err != nil {
		return
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return
	}
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		s.checkLink(c.ID, c.Username, c.RecipientID)
	}()
}

func (s *server) checkLink(codeID, user, recipient string) {
	select {
	case s.work <- struct{}{}:
		defer func() { <-s.work }()
	default:
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 35*time.Second)
	defer cancel()
	record, err := checkout.CheckLink(ctx, s.vault, s.accounts, recipient)
	s.applyLinkState(codeID, user, record, err)
}

func (s *server) applyLinkState(codeID, user string, record *checkout.Record, err error) {
	if record != nil && record.Status == "succeeded" {
		msg := fmt.Sprintf("已为 @%s 完成 %d 个月 Premium 赠送。", user, record.Months)
		if _, dbErr := s.db.Exec("UPDATE codes SET status='succeeded',link_state=?,progress=100,message=?,updated=? WHERE id=? AND status='processing'", linkPaid, msg, time.Now().Unix(), codeID); dbErr != nil {
			log.Printf("order %s success could not be saved", codeID)
		}
		s.logs.Event(oplog.Entry{Kind: "order", Level: "info", Message: "payment confirmed", Extra: map[string]any{"order": codeID, "account": user}})
		return
	}
	if errors.Is(err, checkout.ErrPublicPaymentDeclined) {
		if _, dbErr := s.db.Exec("UPDATE codes SET link_state=?,message=?,updated=? WHERE id=? AND status='processing'", linkDeclined, "付款被支付机构拒绝，本次兑换未完成。请点击「重新生成链接」，每个兑换码仅可重新生成一次。", time.Now().Unix(), codeID); dbErr != nil {
			log.Printf("order %s decline could not be saved", codeID)
		}
		s.logs.Event(oplog.Entry{Kind: "order", Level: "warn", Message: "payment declined by the issuer", Extra: map[string]any{"order": codeID}})
		return
	}
	if errors.Is(err, checkout.ErrVerifyUnpaid) {
		if _, dbErr := s.db.Exec("UPDATE codes SET link_state=?,message=?,updated=? WHERE id=? AND status='processing'", linkExpired, "付款链接已过期，尚未检测到付款。请点击「重新生成链接」，每个兑换码仅可重新生成一次。", time.Now().Unix(), codeID); dbErr != nil {
			log.Printf("order %s expiry could not be saved", codeID)
		}
	}
}

// reconcileLoop refreshes pending links in the background so a customer sees
// completion even without an open page.
func (s *server) reconcileLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		var id, user, recipient string
		err := s.db.QueryRow("SELECT id,username,COALESCE(recipient_id,'') FROM codes WHERE status='processing' AND stripe_session<>'' AND recipient_id IS NOT NULL AND link_checked<? ORDER BY link_checked,updated LIMIT 1", time.Now().Unix()-20).Scan(&id, &user, &recipient)
		if err != nil || recipient == "" {
			continue
		}
		if _, err = s.db.Exec("UPDATE codes SET link_checked=? WHERE id=?", time.Now().Unix(), id); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 12*time.Second)
		select {
		case s.work <- struct{}{}:
			record, checkErr := checkout.CheckLink(ctx, s.vault, s.accounts, recipient)
			s.applyLinkState(id, user, record, checkErr)
			<-s.work
		default:
		}
		cancel()
	}
}

// manualLink lets the administrator generate or reuse a checkout link for any
// username, without a redemption code.
func (s *server) manualLink(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Username string `json:"username"`
		Months   int    `json:"months"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !usernamePattern.MatchString(q.Username) || q.Months < 1 || q.Months > 24 {
		message(w, 400, "请填写正确的 X 用户名并选择套餐时长。")
		return
	}
	if ready, err := s.paymentsAvailable(); !ready || err != nil {
		message(w, http.StatusServiceUnavailable, "充值通道尚未就绪，请先配置 X 账号池。")
		return
	}
	select {
	case s.work <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "3")
		message(w, 409, "有订单正在处理，请稍后再生成链接。")
		return
	}
	defer func() { <-s.work }()
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		message(w, 503, "无法锁定订单，请稍后重试。")
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		w.Header().Set("Retry-After", "3")
		message(w, 409, "有订单正在处理，请稍后再生成链接。")
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(r.Context(), 170*time.Second)
	defer cancel()
	record, err := checkout.CreateAdminLink(ctx, s.vault, s.accounts, q.Username, q.Months)
	if err != nil {
		log.Printf("manual link failed: username=%s months=%d reason=%s", q.Username, q.Months, manualLinkFailureReason(err))
		switch {
		case errors.Is(err, checkout.ErrCheckoutRateLimited):
			seconds := 15
			var wait *checkout.CheckoutWaitError
			if errors.As(err, &wait) {
				seconds = int((wait.Wait + time.Second - 1) / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("X-Checkout-Wait-Seconds", strconv.Itoa(seconds))
			}
			w.Header().Set("Retry-After", strconv.Itoa(min(seconds, 10)))
			message(w, 429, "正在等待通道空闲，请稍候重试。")
		case errors.Is(err, checkout.ErrPublicPaymentInProgress):
			message(w, 409, "该订单正在付款或银行验证中，请先完成当前付款。")
		case errors.Is(err, checkout.ErrVerifyUnpaid):
			message(w, 409, "原付款链接已失效，请确认原订单未付款后重新生成。")
		case errors.Is(err, checkout.ErrManualLinkConflict):
			message(w, 409, "该客户已有其他套餐或账号信息的订单，请先通过客户查询核实原订单。")
		case errors.Is(err, checkout.ErrNotEligible):
			message(w, 409, "该账号目前无法接收 Premium 赠送。")
		case errors.Is(err, checkout.ErrUserNotFound):
			message(w, 404, "未找到该 X 用户名。")
		case errors.Is(err, checkout.ErrNoAccount):
			message(w, 503, "没有可用的 X 账号，请先在后台配置账号池。")
		default:
			message(w, 502, "暂时无法生成付款链接，请稍后重试；重复请求会优先检查已有订单。")
		}
		return
	}
	if record == nil {
		message(w, 502, "未取得有效订单。")
		return
	}
	result := map[string]any{"username": record.Username, "months": record.Months, "amount": record.Amount, "currency": record.Currency, "status": record.Status}
	if record.Status == "succeeded" {
		result["message"] = "该客户的这笔订单已付款成功，无需再次付款。"
	} else {
		link := checkout.CheckoutLink(record)
		if link == "" {
			message(w, 502, "未取得有效付款链接。")
			return
		}
		result["checkout_url"] = link
		result["expires_at"] = record.Created + int64(checkout.PublicLinkTTL/time.Second)
	}
	reply(w, 200, result)
}

func manualLinkFailureReason(err error) string {
	switch {
	case errors.Is(err, checkout.ErrCheckoutRateLimited):
		return "creation_rate_limited"
	case errors.Is(err, checkout.ErrPublicPaymentInProgress):
		return "payment_in_progress"
	case errors.Is(err, checkout.ErrPublicLinkPending):
		return "creation_pending"
	case errors.Is(err, checkout.ErrPublicLinkConflict):
		return "order_conflict"
	case errors.Is(err, checkout.ErrVerifyUnpaid):
		return "requires_unpaid_confirmation"
	case errors.Is(err, checkout.ErrNotEligible):
		return "ineligible"
	case errors.Is(err, checkout.ErrUserNotFound):
		return "user_not_found"
	case errors.Is(err, checkout.ErrXReadFailure):
		return "x_read_failure"
	case errors.Is(err, checkout.ErrNoAccount):
		return "no_account"
	default:
		return "upstream_or_order_verification"
	}
}
