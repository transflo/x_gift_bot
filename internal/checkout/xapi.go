package checkout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"xgift/internal/accounts"
	"xgift/internal/store"
)

type Plan struct {
	Months, Minor int
	ProductID     string
	Merchant      string
	Currency      string
}

var ErrNotEligible = errors.New("recipient cannot receive Premium gifts")
var ErrUserNotFound = errors.New("recipient was not found")
var ErrXReadFailure = errors.New("X account or price query failed")

// Eligibility is read-only: it neither creates a checkout nor publishes a
// link. Any enabled account may serve the check.
func Eligibility(ctx context.Context, v *store.Store, mgr *accounts.Manager, user string) (string, error) {
	list, err := accounts.Load(v)
	if err != nil {
		return "", err
	}
	enabled := accounts.Enabled(list)
	if len(enabled) == 0 {
		return "", errors.New("no enabled X account is configured")
	}
	c, err := newXClient(ctx, v, mgr, enabled[0])
	if err != nil {
		return "", err
	}
	defer c.close()
	return c.recipient(ctx, user)
}

func (p Plan) Name() string { return fmt.Sprintf("Premium Gift - %d months", p.Months) }

type xClient struct {
	// Set only after validating an explicitly replaced checkout.
	publicReplacement string
	account           accounts.Account
	mgr               *accounts.Manager
	records             *store.Store
	http              *http.Client
	headers           http.Header
}

func newXClient(ctx context.Context, v *store.Store, mgr *accounts.Manager, account accounts.Account) (*xClient, error) {
	raw, e := v.Get("api-auth")
	if e != nil {
		return nil, errors.New("X API authentication metadata is missing or unreadable")
	}
	defer clear(raw)
	var auth struct{ Authorization, UserAgent string }
	if json.Unmarshal(raw, &auth) != nil || !strings.HasPrefix(auth.Authorization, "Bearer ") {
		return nil, errors.New("invalid X API authentication metadata")
	}
	if err := account.Validate(); err != nil {
		return nil, err
	}
	h := http.Header{"Authorization": {auth.Authorization}, "User-Agent": {auth.UserAgent}, "Content-Type": {"application/json"}, "Origin": {"https://x.com"}, "X-Twitter-Auth-Type": {"OAuth2Session"}, "X-Twitter-Active-User": {"yes"}, "X-Twitter-Client-Language": {"en"}}
	h.Add("Cookie", "auth_token="+account.AuthToken)
	h.Add("Cookie", "ct0="+account.CT0)
	h.Set("X-Csrf-Token", account.CT0)
	base, err := mgr.Client(account)
	if err != nil {
		return nil, err
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("unexpected X API redirect") }
	return &xClient{account: account, mgr: mgr, http: &client, headers: h, records: v}, nil
}

func (c *xClient) stripe(ctx context.Context) (*stripeClient, error) {
	return newStripe(ctx, c.records, c.mgr, c.account)
}

func (c *xClient) close() { c.http.CloseIdleConnections() }

func (c *xClient) call(ctx context.Context, user, name, id string, variables any, mutation bool, out any) error {
	if mutation {
		return c.callOnce(ctx, user, name, id, variables, true, out)
	}
	return retrySafe(ctx, func() error { return c.callOnce(ctx, user, name, id, variables, false, out) })
}

func (c *xClient) callOnce(ctx context.Context, user, name, id string, variables any, mutation bool, out any) (callErr error) {
	// A fixed-operation audit is written before a mutation is sent, then
	// completed even on transport/body failures. Headers and cookies are never
	// stored here.
	var audit struct {
		AccountID  string `json:"account_id"`
		Operation  string `json:"operation"`
		Variables  any    `json:"variables"`
		StartedAt  int64  `json:"started_at"`
		FinishedAt int64  `json:"finished_at,omitempty"`
		Phase      string `json:"phase"`
		HTTP       int    `json:"http_status,omitempty"`
		Cause      string `json:"cause,omitempty"`
		Failure    string `json:"failure,omitempty"`
		RequestID  string `json:"request_id,omitempty"`
	}
	audit.AccountID, audit.Operation, audit.Variables, audit.StartedAt, audit.Phase = c.account.ID, name, variables, time.Now().Unix(), "request_pending"
	if !mutation {
		defer func() {
			if callErr == nil {
				return
			}
			audit.FinishedAt, audit.Failure = time.Now().Unix(), callErr.Error()
			b, err := json.Marshal(audit)
			defer clear(b)
			if err != nil || c.records.Put(fmt.Sprintf("x-read-failure:%s:%s:%d", user, name, time.Now().UnixNano()), b) != nil {
				callErr = fmt.Errorf("%w: could not preserve failure details", ErrXReadFailure)
				return
			}
			callErr = fmt.Errorf("%w: %w", ErrXReadFailure, callErr)
		}()
	}
	if mutation {
		key := fmt.Sprintf("x-create-attempt:%s:%d", user, time.Now().UnixNano())
		persist := func() error {
			b, e := json.Marshal(audit)
			if e != nil {
				return e
			}
			defer clear(b)
			return c.records.Put(key, b)
		}
		if e := persist(); e != nil {
			return errors.New("could not preserve X creation attempt before sending")
		}
		defer func() {
			audit.FinishedAt = time.Now().Unix()
			if callErr != nil {
				audit.Failure = callErr.Error()
			}
			if e := persist(); e != nil {
				callErr = errors.New("could not preserve X creation outcome; automatic recovery blocked")
			}
		}()
	}
	target := "https://x.com/i/api/graphql/" + id + "/" + name
	method := http.MethodGet
	var body []byte
	if mutation {
		method = http.MethodPost
		body, _ = json.Marshal(map[string]any{"variables": variables, "queryId": id})
	} else {
		b, _ := json.Marshal(variables)
		q := url.Values{"variables": {string(b)}}
		if name == "useSubscriptionProductDetailsByRestIdQuery" {
			q.Set("features", `{"subscriptions_marketing_page_fetch_promotions":true}`)
		}
		target += "?" + q.Encode()
	}
	req, e := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Referer", "https://x.com/"+user+"/gift-premium")
	res, e := c.http.Do(req)
	if e != nil {
		audit.Phase, audit.Cause = "transport_failed", e.Error()
		return temporary(&transportFailure{cause: fmt.Errorf("X %s request failed", name)})
	}
	audit.HTTP, audit.Phase = res.StatusCode, "response_received"
	audit.RequestID = res.Header.Get("X-Request-ID")
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	defer clear(raw)
	if len(raw) > 2<<20 {
		audit.Phase = "response_too_large"
		return errors.New("X response exceeded size limit")
	}
	if e != nil {
		audit.Phase, audit.Cause = "response_read_failed", e.Error()
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			return xHTTPFailure(errors.New("X error response could not be read"), res.StatusCode, res.Header.Get("Retry-After"), mutation)
		}
		return temporary(errors.New("X response could not be read"))
	}
	audit.Phase = "response_validation"
	if res.StatusCode != 200 {
		return xHTTPFailure(fmt.Errorf("X %s returned HTTP %d; no payment attempted", name, res.StatusCode), res.StatusCode, res.Header.Get("Retry-After"), mutation)
	}
	var envelope struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return errors.New("X returned non-JSON data")
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("X %s rejected the operation", name)
	}
	if e = json.Unmarshal(raw, out); e != nil {
		return e
	}
	audit.Phase = "response_decoded"
	return nil
}

// A 403 on a read-only X query can be transient. Retry within the existing
// bounded budget and Retry-After rules, without changing credentials or proxy.
// Never apply this exception to checkout creation.
func xHTTPFailure(err error, status int, retryAfter string, mutation bool) error {
	wrapped := &statusFailure{cause: err, status: status}
	if status == http.StatusForbidden && !mutation {
		return httpFailure(wrapped, http.StatusTooManyRequests, retryAfter)
	}
	return httpFailure(wrapped, status, retryAfter)
}

func (c *xClient) recipient(ctx context.Context, user string) (string, error) {
	return c.identity(ctx, user, true)
}

func (c *xClient) identity(ctx context.Context, user string, requireEligible bool) (string, error) {
	var r struct {
		Data struct {
			User struct {
				Result struct {
					ID       string `json:"rest_id"`
					Eligible bool   `json:"premium_gifting_eligible"`
					Core     struct {
						Screen string `json:"screen_name"`
					} `json:"core"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "PremiumGiftingQuery", "kn8hCE6bHstQV2MtfYDTKg", map[string]string{"screenName": user}, false, &r); e != nil {
		return "", e
	}
	u := r.Data.User.Result
	if u.ID == "" {
		return "", ErrUserNotFound
	}
	if !strings.EqualFold(u.Core.Screen, user) {
		return "", errors.New("recipient identity or gift eligibility could not be verified")
	}
	if requireEligible && !u.Eligible {
		return "", ErrNotEligible
	}
	return u.ID, nil
}

func (c *xClient) quote(ctx context.Context, user string, p Plan) error {
	var r struct {
		Data struct {
			Product struct {
				ID     string `json:"rest_id"`
				Prices []struct {
					Amount   int64  `json:"amount_local_micro"`
					Currency string `json:"currency_code"`
					Type     string `json:"price_type"`
				} `json:"prices"`
			} `json:"web_subscription_product_details_by_rest_id"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "useSubscriptionProductDetailsByRestIdQuery", "Se1Bp6zcNnuXYXRecV2qLA", map[string]string{"stripeId": p.ProductID}, false, &r); e != nil {
		return e
	}
	product := r.Data.Product
	if product.ID != p.ProductID || len(product.Prices) != 1 {
		return errors.New("unexpected X product or price list")
	}
	price := product.Prices[0]
	if price.Type != "OneTime" || !strings.EqualFold(price.Currency, p.Currency) || price.Amount != int64(p.Minor)*10000 {
		return errors.New("X price is not exactly the allowed one-time amount")
	}
	return nil
}

func (c *xClient) create(ctx context.Context, user, recipient string, p Plan) (string, string, error) {
	var r struct {
		Data struct {
			Gift struct {
				ID     string `json:"session_id"`
				URL    string `json:"session_url"`
				Status string `json:"session_status"`
			} `json:"onetimepurchase_gift"`
		} `json:"data"`
	}
	variables := map[string]string{"cancel_url": "https://x.com/" + user + "/gift-premium", "success_url": "https://x.com/" + user + "/gift-premium/success", "external_product_id": p.ProductID, "gift_recipient": recipient}
	if e := c.call(ctx, user, "useOneTimePurchaseGiftMutation", "GqTVJ4S1526tLkxj69xIZw", variables, true, &r); e != nil {
		return "", "", e
	}
	s := r.Data.Gift
	if s.Status != "Unpaid" {
		return "", "", errors.New("X checkout status is not Unpaid; no link was published")
	}
	if !sessionPattern.MatchString(s.ID) {
		return "", "", errors.New("X checkout session ID is missing or not a live session; no link was published")
	}
	if !sessionURL(s.URL, s.ID) {
		return "", "", errors.New("X checkout URL is unsupported or does not match its session; no link was published")
	}
	return s.ID, s.URL, nil
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)
