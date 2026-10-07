package site

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"xgift/internal/checkout"
)

// customerOrder is independent of folder filters and pagination. It returns
// the code state plus the currently bound Stripe link, without ever exposing
// account credentials.
func (s *server) customerOrder(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	user := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.URL.Query().Get("username")), "@"))
	var c codeRow
	var digest string
	query := "SELECT " + codeColumns + ",hash FROM codes WHERE "
	var arg string
	if id != "" {
		if !folderIDPattern.MatchString(id) {
			message(w, 400, "订单编号无效。")
			return
		}
		query += "id=?"
		arg = id
	} else {
		if !usernamePattern.MatchString(user) {
			message(w, 400, "请输入正确的客户 X 用户名。")
			return
		}
		query += "username=?"
		arg = user
	}
	var regenerated int
	err := s.db.QueryRow(query, arg).Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.RecipientID, &c.StripeURL, &c.StripeSession, &c.LinkCreated, &regenerated, &c.AccountID, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		message(w, 404, "未找到这个客户的订单。")
		return
	}
	if err != nil {
		message(w, 503, "无法读取客户订单。")
		return
	}
	c.LinkRegenerated = regenerated != 0
	decorate(&c)
	plain := ""
	if c.Copyable {
		b, e := s.records.Get("redemption:" + c.ID)
		if e != nil {
			message(w, 503, "无法读取卡密，请稍后重试。")
			return
		}
		defer clear(b)
		if !codePattern.Match(b) || hash(string(b)) != digest {
			message(w, 503, "卡密校验未通过，请核实加密记录。")
			return
		}
		plain = string(b)
	}
	link := c.StripeURL
	if link == "" && c.RecipientID != "" {
		if record, e := checkout.LoadRecord(s.records, c.RecipientID); e == nil {
			link = checkout.CheckoutLink(record)
		}
	}
	reply(w, 200, map[string]any{"order": c, "code": plain, "checkout_url": link, "can_regenerate": canRegenerate(c)})
}
