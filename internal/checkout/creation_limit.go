package checkout

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"xgift/internal/vault"
)

var ErrCheckoutRateLimited = errors.New("checkout creation must be spaced at least 15 seconds apart per account")

const checkoutCreationInterval = 15 * time.Second

func lastCreationKey(accountID string) string { return "checkout-creation:last:" + accountID }

// reserveCheckoutCreation is called under checkout.lock immediately before an
// X mutation. Persist before sending: failures and restarts must not bypass
// the per-account spacing.
func reserveCheckoutCreation(v *vault.Vault, accountID string, now time.Time) error {
	if err := checkCheckoutCreation(v, accountID, now); err != nil {
		return err
	}
	b, _ := json.Marshal(now.UnixMilli())
	return v.Put(lastCreationKey(accountID), b)
}

func checkCheckoutCreation(v *vault.Vault, accountID string, now time.Time) error {
	b, err := v.Get(lastCreationKey(accountID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var last int64
		if err = json.Unmarshal(b, &last); err != nil {
			return err
		}
		if now.Sub(time.UnixMilli(last)) < checkoutCreationInterval {
			return &CheckoutWaitError{Wait: time.UnixMilli(last).Add(checkoutCreationInterval).Sub(now)}
		}
	}
	return nil
}

// LastCreation reports the persisted creation time for load balancing.
func LastCreation(v *vault.Vault, accountID string) time.Time {
	b, err := v.Get(lastCreationKey(accountID))
	if err != nil {
		return time.Time{}
	}
	var last int64
	if json.Unmarshal(b, &last) != nil {
		return time.Time{}
	}
	return time.UnixMilli(last)
}
