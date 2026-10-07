package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"xgift/internal/vault"
)

var (
	ErrPublicLinkConflict      = errors.New("existing checkout requires private review")
	ErrPublicLinkPending       = errors.New("checkout creation returned no usable link")
	ErrPublicPaymentDeclined   = errors.New("checkout payment declined; a new link is required")
	ErrPublicPaymentInProgress = errors.New("checkout payment is in progress")
	ErrVerifyUnpaid            = errors.New("existing checkout must be verified unpaid before replacement")
)

func publicLinkFresh(r *Record, now time.Time) bool {
	created := time.Unix(r.Created, 0)
	return r.Created > 0 && !now.Before(created) && now.Sub(created) < publicLinkTTL
}

// verifyPublicCheckout reads the session through Stripe and classifies its
// state. It never tokens a card, confirms a payment or cancels anything.
func verifyPublicCheckout(ctx context.Context, v *vault.Vault, x *xClient, r *Record, plan Plan) error {
	s, err := x.stripe(ctx)
	if err != nil {
		return err
	}
	defer s.close()
	p, err := s.page(ctx, r, true)
	if inactiveCheckout(err) {
		if paid, _ := x.checkoutPaid(ctx, r, plan); paid {
			r.Status = "succeeded"
			return nil
		}
		return ErrVerifyUnpaid
	}
	if err != nil {
		return err
	}
	if err = p.guard(r, plan, false); err != nil {
		return err
	}
	if err = rememberVerifiedCheckout(v, r, plan, p); err != nil {
		return err
	}
	if p.Status == "complete" && p.PaymentStatus == "paid" {
		r.Status = "succeeded"
		return nil
	}
	if p.Status == "expired" && p.PaymentStatus == "unpaid" && p.IntentPresent && p.IntentNull && p.Intent == nil {
		return ErrVerifyUnpaid
	}
	if p.Intent != nil {
		if publicIntentDeclined(p) {
			return ErrPublicPaymentDeclined
		}
		if publicIntentIdle(p) {
			if p.Status == "expired" && p.PaymentStatus == "unpaid" {
				return ErrVerifyUnpaid
			}
			manual := *p
			manual.Intent, manual.IntentNull, manual.IntentPresent = nil, true, true
			return manual.guard(r, plan, true)
		}
		return ErrPublicPaymentInProgress
	}
	return p.guard(r, plan, true)
}

// Only explicit refusal evidence can end an otherwise valid payment window.
// An unused intent also requires_payment_method, so that status alone is
// insufficient.
func publicIntentDeclined(p *paymentPage) bool {
	if !publicIntentIdle(p) {
		return false
	}
	var extra struct {
		Intent struct {
			Error struct {
				Code        string `json:"code"`
				DeclineCode string `json:"decline_code"`
			} `json:"last_payment_error"`
		} `json:"payment_intent"`
	}
	if json.Unmarshal(p.raw, &extra) != nil {
		return false
	}
	return extra.Intent.Error.Code == "card_declined" || extra.Intent.Error.DeclineCode != ""
}

// Stripe's publishable-key responses omit amount_received/capturable. The
// live, guarded requires_payment_method/canceled status proves no payment is
// in flight; missing private fields are not evidence of processing.
func publicIntentIdle(p *paymentPage) bool {
	if p.Intent == nil || p.PaymentStatus != "unpaid" || (p.Intent.Status != "requires_payment_method" && p.Intent.Status != "canceled") || (p.Intent.AmountReceived != nil && *p.Intent.AmountReceived != 0) {
		return false
	}
	if len(p.raw) > 0 {
		var extra struct {
			Intent struct {
				Capturable *int `json:"amount_capturable"`
			} `json:"payment_intent"`
		}
		if json.Unmarshal(p.raw, &extra) != nil || (extra.Intent.Capturable != nil && *extra.Intent.Capturable != 0) {
			return false
		}
	}
	return true
}

func inactiveCheckout(err error) bool {
	var se *stripeError
	return errors.As(err, &se) && (se.Code == "resource_missing" || se.HTTP == 404)
}
