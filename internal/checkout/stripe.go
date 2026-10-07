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
	"xgift/internal/vault"
)

// stripeClient performs read-only Stripe requests through the same proxy as
// the X account that created the session. There is deliberately no card
// tokenization or payment confirmation in this package.
type stripeClient struct {
	http  *http.Client
	key   string
	vault *vault.Vault
}

type stripeError struct {
	Code, Type                string
	Message, Param, RequestID string
	HTTP                      int
}

func (e *stripeError) Error() string {
	return fmt.Sprintf("Stripe rejected the request (HTTP %d, type=%s, code=%s, param=%s, request=%s): %s", e.HTTP, e.Type, e.Code, e.Param, e.RequestID, e.Message)
}

var errStripeRedirect = errors.New("unexpected Stripe API redirect")

type stripeTransportFailure struct{}

func (*stripeTransportFailure) Error() string {
	return "Stripe transport failed; request outcome may be unknown"
}

func newStripe(ctx context.Context, v *vault.Vault, mgr *accounts.Manager, account accounts.Account) (*stripeClient, error) {
	key, err := v.Get("stripe-key")
	if err != nil {
		return nil, errors.New("Stripe publishable key is missing; store it with setup or put --name stripe-key")
	}
	defer clear(key)
	if !ValidStripeKey(string(key)) {
		return nil, errors.New("invalid Stripe merchant publishable key")
	}
	base, err := mgr.Client(account)
	if err != nil {
		return nil, err
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errStripeRedirect }
	return &stripeClient{http: &client, key: string(key), vault: v}, nil
}

func (s *stripeClient) close() {
	s.http.CloseIdleConnections()
}

func (s *stripeClient) call(ctx context.Context, method, path string, form url.Values, out any) error {
	form.Set("key", s.key)
	target := "https://api.stripe.com/v1/" + path
	var input io.Reader
	if method == http.MethodGet {
		target += "?" + form.Encode()
	} else {
		input = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, target, input)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := s.http.Do(req)
	if e != nil {
		if errors.Is(e, errStripeRedirect) {
			return e
		}
		return temporary(&stripeTransportFailure{})
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if e != nil {
		if res.StatusCode >= 400 {
			return httpFailure(errors.New("Stripe error response could not be read"), res.StatusCode, res.Header.Get("Retry-After"))
		}
		return temporary(&stripeTransportFailure{})
	}
	defer clear(raw)
	var envelope struct {
		Error *stripeAPIError `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return httpFailure(errors.New("Stripe returned non-JSON data"), res.StatusCode, res.Header.Get("Retry-After"))
	}
	if envelope.Error != nil {
		msg := envelope.Error.Message
		msg = regexp.MustCompile(`[0-9]{12,19}|(?:cs_live_|pm_|pi_|pk_live_|sk_live_)[A-Za-z0-9_]+`).ReplaceAllString(msg, "[redacted]")
		msg = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, msg)
		if len(msg) > 600 {
			msg = msg[:600]
		}
		se := &stripeError{Code: safeErrorField(envelope.Error.Code), Type: safeErrorField(envelope.Error.Type), HTTP: res.StatusCode, Message: msg, Param: safeErrorField(envelope.Error.Param), RequestID: safeErrorField(res.Header.Get("Request-Id"))}
		diagnostic, _ := json.Marshal(map[string]any{"http_status": res.StatusCode, "path": path, "error": se, "observed_at": time.Now().Unix()})
		defer clear(diagnostic)
		if err := s.vault.Put("stripe-error:last", diagnostic); err != nil {
			return errors.New("could not persist Stripe error; order requires inspection")
		}
		parts := strings.Split(path, "/")
		if len(parts) >= 2 && parts[0] == "payment_pages" && sessionPattern.MatchString(parts[1]) {
			if err := s.vault.Put("stripe-error:"+parts[1], diagnostic); err != nil {
				return errors.New("could not persist order payment error")
			}
		}
		return httpFailure(se, res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res.StatusCode != 200 {
		return httpFailure(&stripeError{HTTP: res.StatusCode}, res.StatusCode, res.Header.Get("Retry-After"))
	}
	return json.Unmarshal(raw, out)
}

// stripeAPIError retains only the fields needed to diagnose a read-only
// session lookup. Payment-method and customer details are never stored.
type stripeAPIError struct {
	Type, Code, Message, Param string
}

type paymentPage struct {
	raw           json.RawMessage
	IntentPresent bool   `json:"-"`
	IntentNull    bool   `json:"-"`
	SessionID     string `json:"session_id"`
	Currency      string `json:"currency"`
	Mode          string `json:"mode"`
	Live          bool   `json:"livemode"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	Checksum      string `json:"init_checksum"`
	SuccessURL    string `json:"success_url"`
	CancelURL     string `json:"cancel_url"`
	Account       struct {
		ID string `json:"account_id"`
	} `json:"account_settings"`
	SetupFuture  json.RawMessage                    `json:"setup_future_usage"`
	Subscription json.RawMessage                    `json:"subscription_data"`
	SetupIntent  json.RawMessage                    `json:"setup_intent"`
	Total        struct{ Due, Subtotal, Total int } `json:"total_summary"`
	Group        struct {
		Currency             string `json:"currency"`
		Due, Subtotal, Total int
		Items                []struct {
			Name                      string `json:"name"`
			Quantity, Subtotal, Total int
			Price                     struct {
				Currency, Type string
				UnitAmount     int             `json:"unit_amount"`
				Recurring      json.RawMessage `json:"recurring"`
				Product        struct {
					ID, Name string
					Live     bool `json:"livemode"`
				} `json:"product"`
			} `json:"price"`
		} `json:"line_items"`
	} `json:"line_item_group"`
	Intent *struct {
		ID, Status, Currency string
		Amount               int
		AmountReceived       *int `json:"amount_received"`
	} `json:"payment_intent"`
}

func (p *paymentPage) UnmarshalJSON(b []byte) error {
	type plain paymentPage
	var value plain
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	*p = paymentPage(value)
	p.raw = append(json.RawMessage(nil), b...)
	raw, ok := fields["payment_intent"]
	p.IntentPresent = ok
	p.IntentNull = ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	return nil
}

func nullJSON(v json.RawMessage) bool { return len(v) == 0 || string(v) == "null" }

// guard verifies the Stripe session is the exact catalog product for the
// recorded recipient. It never submits a payment.
func (p *paymentPage) guard(r *Record, plan Plan, before bool) error {
	if p.SessionID != r.SessionID || p.Account.ID != plan.Merchant || !p.Live || p.Mode != "payment" || p.Currency != plan.Currency || p.Group.Currency != plan.Currency || p.SuccessURL != "https://x.com/"+r.Username+"/gift-premium/success" || p.CancelURL != "https://x.com/"+r.Username+"/gift-premium" {
		return errors.New("Stripe merchant, session, recipient return URLs, currency or payment mode mismatch")
	}
	if !nullJSON(p.SetupFuture) || !nullJSON(p.Subscription) || !nullJSON(p.SetupIntent) {
		return errors.New("recurring payments or saved-card setup are not allowed")
	}
	if p.Total.Total != plan.Minor || p.Total.Subtotal != plan.Minor || p.Group.Total != plan.Minor || p.Group.Subtotal != plan.Minor || len(p.Group.Items) != 1 {
		return errors.New("Stripe final total or line item count does not match the exact allowed price")
	}
	item := p.Group.Items[0]
	if item.Name != plan.Name() || item.Quantity != 1 || item.Subtotal != plan.Minor || item.Total != plan.Minor || item.Price.Currency != plan.Currency || item.Price.Type != "one_time" || item.Price.UnitAmount != plan.Minor || !nullJSON(item.Price.Recurring) || item.Price.Product.ID != plan.ProductID || item.Price.Product.Name != plan.Name() || !item.Price.Product.Live {
		return errors.New("Stripe product, duration, quantity or unit amount mismatch")
	}
	if p.Intent != nil {
		if p.Intent.Currency != plan.Currency || p.Intent.Amount != plan.Minor {
			return errors.New("Stripe payment intent amount or currency mismatch")
		}
		if p.PaymentStatus == "paid" && (p.Intent.Status != "succeeded" || (p.Intent.AmountReceived == nil || *p.Intent.AmountReceived != plan.Minor)) {
			return errors.New("Stripe payment intent does not confirm the exact received amount")
		}
	}
	if before && (!p.IntentPresent || !p.IntentNull) {
		return errors.New("Stripe must explicitly return a null payment intent before publishing a link")
	}
	if before && (p.Status != "open" || p.PaymentStatus != "unpaid" || p.Total.Due != plan.Minor || p.Group.Due != plan.Minor || p.Checksum == "") {
		return errors.New("Stripe checkout is not open and unpaid at the exact authorized amount")
	}
	return nil
}

// page reads a session. init=true performs the same read call the checkout
// page performs; init=false is used once a session completed.
func (s *stripeClient) page(ctx context.Context, r *Record, init bool) (*paymentPage, error) {
	method, path := http.MethodGet, "payment_pages/"+r.SessionID
	form := url.Values{}
	if init {
		method = http.MethodPost
		path += "/init"
		form.Set("browser_locale", "en")
		form.Set("redirect_type", "url")
	}
	var page paymentPage
	e := retrySafe(ctx, func() error { return s.call(ctx, method, path, form, &page) })
	return &page, e
}

func safeErrorField(s string) string {
	if len(s) > 100 {
		return "[redacted]"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.\[\]-]*$`).MatchString(s) {
		return "[redacted]"
	}
	return s
}
