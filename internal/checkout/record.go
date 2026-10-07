package checkout

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"

	"xgift/internal/store"
)

// Record is the durable description of one Stripe checkout session
// created through an X account. It never contains card data or payment secrets.
type Record struct {
	AccountID         string `json:"account_id,omitempty"`
	CreationRetryable bool   `json:"creation_retryable,omitempty"`
	CreationAttempts  int    `json:"creation_attempts,omitempty"`

	Username    string `json:"username"`
	RecipientID string `json:"recipient_id"`
	Months      int    `json:"months"`
	Amount      int    `json:"amount_minor"`
	Currency    string `json:"currency"`
	ProductID   string `json:"product_id"`
	SessionID   string `json:"session_id"`
	URL         string `json:"url"`
	Status      string `json:"status"`
	Created     int64  `json:"created"`
}

var ErrSessionMismatch = errors.New("checkout session is missing or malformed")

var sessionPattern = regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`)
var sessionPathPattern = regexp.MustCompile(`^/[A-Za-z]/pay/`)

func sessionURL(link, id string) bool {
	u, e := url.Parse(link)
	return e == nil && sessionPattern.MatchString(id) && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && u.RawQuery == "" && sessionPathPattern.MatchString(u.Path) && u.Path[2:] == "/pay/"+id
}

// CheckoutLink returns the payable URL only when it matches its session id.
func CheckoutLink(r *Record) string {
	if r == nil || !sessionURL(r.URL, r.SessionID) {
		return ""
	}
	return r.URL
}

func recordKey(recipient string) string { return "checkout:" + recipient }

func save(v *store.Store, r *Record) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return v.Put(recordKey(r.RecipientID), b)
}

// LoadRecord reads one recipient's checkout record. sql.ErrNoRows means none.
func LoadRecord(v *store.Store, recipient string) (*Record, error) {
	raw, err := v.Get(recordKey(recipient))
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var r Record
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, ErrSessionMismatch
	}
	if r.RecipientID != recipient {
		return nil, ErrSessionMismatch
	}
	return &r, nil
}

func recordMatches(r *Record, user, recipient string, p Plan) bool {
	return r.Username == user && r.RecipientID == recipient && r.Months == p.Months &&
		r.Amount == p.Minor && r.Currency == upperCurrency(p.Currency) && r.ProductID == p.ProductID
}

func upperCurrency(c string) string {
	b := []byte(c)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}
