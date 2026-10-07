package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"xgift/internal/vault"
)

const activeCheckoutPrefix = "checkout-creation:active:"

// PublicLinkTTL is the fixed payment window, measured from order creation.
const PublicLinkTTL = 3 * time.Minute

const publicLinkTTL = PublicLinkTTL

// CheckoutWaitError is a retryable creation wait. Reading an existing verified
// link does not require a new creation reservation.
type CheckoutWaitError struct{ Wait time.Duration }

func (e *CheckoutWaitError) Error() string {
	return "checkout creation is waiting for the active payment window"
}
func (e *CheckoutWaitError) Unwrap() error { return ErrCheckoutRateLimited }

// activeCheckout is one account's unpaid session window. X allows a single
// unpaid gift checkout per account, so the window is keyed by account.
type activeCheckout struct {
	Order     Record `json:"order"`
	Plan      Plan   `json:"plan"`
	ExpiresAt int64  `json:"expires_at"`
	Released  bool   `json:"released"`
}

func activeKey(accountID string) string { return activeCheckoutPrefix + accountID }

func saveActiveCheckout(v *vault.Vault, accountID string, a activeCheckout) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return v.Put(activeKey(accountID), b)
}

func readActiveCheckout(v *vault.Vault, accountID string) (activeCheckout, error) {
	var a activeCheckout
	b, err := v.Get(activeKey(accountID))
	if err != nil {
		return a, err
	}
	defer clear(b)
	if err = json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	deadline := time.Unix(a.Order.Created, 0).Add(publicLinkTTL).UnixMilli()
	legacyDeadline := time.Unix(a.Order.Created, 0).Add(15 * time.Minute).UnixMilli()
	if a.ExpiresAt != 0 && (a.Order.SessionID == "" || a.Order.Created <= 0 || (a.ExpiresAt != deadline && a.ExpiresAt != legacyDeadline)) {
		return a, errors.New("invalid active checkout reservation")
	}
	if a.ExpiresAt != 0 {
		a.ExpiresAt = deadline
	}
	return a, nil
}

// holdPublicCheckout reserves the account's unpaid window for a freshly
// published link. Callers hold checkout.lock.
func holdPublicCheckout(v *vault.Vault, accountID string, r *Record, p Plan, now time.Time) error {
	if r.Status != "created" || !publicLinkFresh(r, now) {
		return nil
	}
	current, err := readActiveCheckout(v, accountID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if !current.Released && current.ExpiresAt > now.UnixMilli() && current.Order.SessionID != r.SessionID {
			return nil
		}
		if current.Order.SessionID == r.SessionID && current.Released {
			return nil
		}
	}
	return saveActiveCheckout(v, accountID, activeCheckout{Order: *r, Plan: p, ExpiresAt: time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()})
}

// CheckoutCreationWait reports the persisted wait for one account without
// contacting Stripe.
func CheckoutCreationWait(v *vault.Vault, accountID string, now time.Time) (time.Duration, error) {
	var wait time.Duration
	a, err := readActiveCheckout(v, accountID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && !a.Released {
		wait = time.UnixMilli(a.ExpiresAt).Sub(now)
	}
	if wait < 0 {
		wait = 0
	}
	b, err := v.Get(lastCreationKey(accountID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		var last int64
		if err = json.Unmarshal(b, &last); err != nil {
			return 0, err
		}
		if remaining := time.UnixMilli(last).Add(checkoutCreationInterval).Sub(now); remaining > wait {
			wait = remaining
		}
	}
	return wait, nil
}

// checkCreation verifies the account's current window before creating a new
// session. A verified paid or declined window is released.
func (x *xClient) checkCreation(ctx context.Context, accountID string, now time.Time) error {
	a, err := readActiveCheckout(x.vault, accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return checkCheckoutCreation(x.vault, accountID, now)
	}
	if err != nil {
		return err
	}
	if !a.Released && a.ExpiresAt != 0 {
		if x.publicReplacement != "" && a.Order.SessionID == x.publicReplacement {
			return checkCheckoutCreation(x.vault, accountID, now)
		}
		r := a.Order
		verificationErr := verifyPublicCheckout(ctx, x.vault, x, &r, a.Plan)
		verified := verificationErr == nil && r.Status == "succeeded"
		if !verified && !errors.Is(verificationErr, ErrPublicPaymentDeclined) {
			paid, _ := x.checkoutPaid(ctx, &r, a.Plan)
			verified = paid
			if paid {
				r.Status = "succeeded"
			}
		}
		switch {
		case verified:
			a.Released, a.Order = true, r
			if err := saveActiveCheckout(x.vault, accountID, a); err != nil {
				return err
			}
		case errors.Is(verificationErr, ErrPublicPaymentDeclined):
			if _, err := releaseActiveCheckout(x.vault, accountID, r.SessionID); err != nil {
				return err
			}
		case time.Until(time.UnixMilli(a.ExpiresAt)) > 0:
			return &CheckoutWaitError{Wait: time.Until(time.UnixMilli(a.ExpiresAt))}
		case verificationErr != nil && !errors.Is(verificationErr, ErrVerifyUnpaid):
			return &CheckoutWaitError{Wait: 10 * time.Second}
		default:
			a.Released = true
			if err := saveActiveCheckout(x.vault, accountID, a); err != nil {
				return err
			}
		}
	}
	return checkCheckoutCreation(x.vault, accountID, now)
}

func (x *xClient) checkoutPaid(ctx context.Context, r *Record, p Plan) (bool, error) {
	return verifiedCheckoutPaid(ctx, x.vault, x, r, p)
}

// releasePublicReplacement frees the account window held by the session the
// operator explicitly replaced.
func (x *xClient) releasePublicReplacement(v *vault.Vault, accountID string) error {
	if x.publicReplacement == "" {
		return nil
	}
	_, err := releaseActiveCheckout(v, accountID, x.publicReplacement)
	return err
}

// releaseActiveCheckout marks one session's window released. Other sessions'
// windows are never touched.
func releaseActiveCheckout(v *vault.Vault, accountID, session string) (bool, error) {
	a, err := readActiveCheckout(v, accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.Released || a.Order.SessionID != session {
		return false, nil
	}
	a.Released = true
	return true, saveActiveCheckout(v, accountID, a)
}
