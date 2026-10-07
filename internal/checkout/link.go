package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xgift/internal/accounts"
	"xgift/internal/store"
)

// ErrNoAccount is returned when the pool has no enabled account.
var ErrNoAccount = errors.New("no enabled X account is configured")

// ErrManualLinkConflict reports an existing order with a different plan.
var ErrManualLinkConflict = errors.New("existing order belongs to another username or plan")

// LinkRequest describes one checkout-link creation. AccountID pins the account
// used by an earlier session (required when replacing a link so the same X
// identity and exit IP are reused); otherwise the least-busy account is picked.
type LinkRequest struct {
	User      string
	Recipient string
	Months    int
	Replace   bool
	AccountID string
}

// CreateLinkForRecipient is the single entry point for publishing a Stripe
// checkout link. It never charges a card: the visitor pays on Stripe.
func CreateLinkForRecipient(ctx context.Context, v *store.Store, mgr *accounts.Manager, req LinkRequest) (*Record, error) {
	user := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.User), "@"))
	if !usernamePattern.MatchString(user) {
		return nil, errors.New("invalid username")
	}
	if req.Recipient == "" {
		return nil, errors.New("recipient is required")
	}
	catalog, err := ReadCatalog(v)
	if err != nil {
		return nil, err
	}
	plan, err := catalog.PlanFor(req.Months)
	if err != nil {
		return nil, err
	}
	list, err := accounts.Load(v)
	if err != nil {
		return nil, err
	}
	enabled := accounts.Enabled(list)
	if len(enabled) == 0 {
		return nil, ErrNoAccount
	}
	existing, err := LoadRecord(v, req.Recipient)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing != nil && !recordMatches(existing, user, req.Recipient, plan) {
		return nil, ErrPublicLinkConflict
	}
	candidates, err := rankAccounts(v, enabled, existing, req.AccountID, mgr.Usable, time.Now())
	if err != nil {
		return nil, err
	}
	// Walk the candidates on account-side failures: a dead proxy or a revoked
	// cookie should cost one retry, not the whole checkout, while healthy
	// accounts sit idle in the pool.
	var account accounts.Account
	var x *xClient
	var recipient string
	var lastErr error
	for _, candidate := range candidates {
		if candidate.wait > 0 {
			continue
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		client, clientErr := newXClient(ctx, v, mgr, candidate.account)
		if clientErr == nil {
			var resolveErr error
			recipient, resolveErr = client.recipient(ctx, user)
			if resolveErr == nil {
				account, x = candidate.account, client
				break
			}
			client.close()
			clientErr = resolveErr
		}
		if !accountFault(clientErr) {
			return nil, clientErr
		}
		mgr.MarkFailed(candidate.account.ID, clientErr)
		lastErr = clientErr
	}
	if x == nil {
		if lastErr != nil {
			return nil, lastErr
		}
		shortest, found := time.Duration(0), false
		for _, candidate := range candidates {
			if !found || candidate.wait < shortest {
				shortest, found = candidate.wait, true
			}
		}
		if !found {
			return nil, ErrNoAccount
		}
		return nil, &CheckoutWaitError{Wait: shortest}
	}
	defer x.close()
	if recipient != req.Recipient {
		return nil, errors.New("recipient identity changed; refusing to create a link")
	}

	r := Record{AccountID: account.ID, Username: user, RecipientID: recipient, Months: plan.Months, Amount: plan.Minor, Currency: upperCurrency(plan.Currency), ProductID: plan.ProductID, Status: "creating", Created: time.Now().Unix()}
	if existing != nil {
		switch existing.Status {
		case "succeeded":
			return existing, nil
		case "created":
			replace, err := verifyExisting(ctx, v, x, existing, plan, req.Replace)
			if err != nil {
				return nil, err
			}
			if !replace {
				return existing, nil
			}
			if CheckoutLink(existing) != "" {
				x.publicReplacement = existing.SessionID
			}
			if err = archiveReplaced(v, existing); err != nil {
				return nil, err
			}
			if err = x.releasePublicReplacement(v, existing.AccountID); err != nil {
				return nil, err
			}
		case "creating":
			// A previous attempt never produced a session. Reuse its record so
			// the persisted attempt budget still applies.
			if existing.SessionID != "" || existing.URL != "" || existing.CreationAttempts >= maxAttempts {
				return nil, ErrPublicLinkConflict
			}
			r = *existing
			r.AccountID, r.Months, r.Amount, r.Currency, r.ProductID = account.ID, plan.Months, plan.Minor, upperCurrency(plan.Currency), plan.ProductID
		default:
			return nil, ErrPublicLinkConflict
		}
	}

	if err = x.checkCreation(ctx, account.ID, time.Now()); err != nil {
		return nil, err
	}
	if err = save(v, &r); err != nil {
		return nil, err
	}
	for r.CreationAttempts < maxAttempts {
		if err = ctx.Err(); err != nil {
			return &r, err
		}
		// Reserve each attempt durably. A lost response can leave an unused
		// external session, but only the verified link is ever published.
		r.CreationAttempts++
		r.CreationRetryable = false
		if err = save(v, &r); err != nil {
			return &r, err
		}
		if err = reserveCheckoutCreation(v, account.ID, time.Now()); err != nil {
			return &r, err
		}
		progress(ctx, 50, "正在向 X 创建专属赠送订单…")
		r.SessionID, r.URL, err = x.create(ctx, user, recipient, plan)
		if err == nil {
			break
		}
		var retry *temporaryError
		if errors.As(err, &retry) && r.CreationAttempts < maxAttempts {
			r.CreationRetryable = true
			if saveErr := save(v, &r); saveErr != nil {
				return &r, saveErr
			}
		}
		if stop := waitRetry(ctx, err, r.CreationAttempts); stop != nil {
			return &r, stop
		}
	}
	if err != nil {
		return &r, ErrPublicLinkPending
	}
	r.Status = "created"
	r.Created = time.Now().Unix()
	if err = save(v, &r); err != nil {
		return &r, err
	}
	x.publicReplacement = ""
	progress(ctx, 70, "正在核验订单金额与收款方…")
	if err = verifyNewLink(ctx, v, x, &r, plan); err != nil {
		return &r, err
	}
	if err = holdPublicCheckout(v, account.ID, &r, plan, time.Now()); err != nil {
		return &r, err
	}
	return &r, nil
}

// CreateAdminLink resolves the username, then reuses or creates its link.
func CreateAdminLink(ctx context.Context, v *store.Store, mgr *accounts.Manager, user string, months int) (*Record, error) {
	user = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(user), "@"))
	if !usernamePattern.MatchString(user) {
		return nil, errors.New("invalid username")
	}
	plan, err := planFor(v, months)
	if err != nil {
		return nil, err
	}
	list, err := accounts.Load(v)
	if err != nil {
		return nil, err
	}
	enabled := accounts.Enabled(list)
	if len(enabled) == 0 {
		return nil, ErrNoAccount
	}
	account, err := pickReadAccount(v, enabled)
	if err != nil {
		return nil, err
	}
	x, err := newXClient(ctx, v, mgr, account)
	if err != nil {
		return nil, err
	}
	recipient, err := x.identity(ctx, user, false)
	x.close()
	if err != nil {
		return nil, err
	}
	existing, err := LoadRecord(v, recipient)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing != nil && !recordMatches(existing, user, recipient, plan) {
		return nil, ErrManualLinkConflict
	}
	return CreateLinkForRecipient(ctx, v, mgr, LinkRequest{User: user, Recipient: recipient, Months: months, AccountID: account.ID})
}

// CheckLink verifies an existing link through its bound account and reports
// the latest state. It never creates or replaces a session.
func CheckLink(ctx context.Context, v *store.Store, mgr *accounts.Manager, recipient string) (*Record, error) {
	r, err := LoadRecord(v, recipient)
	if err != nil {
		return nil, err
	}
	plan, err := planFor(v, r.Months)
	if err != nil {
		return r, err
	}
	list, err := accounts.Load(v)
	if err != nil {
		return r, err
	}
	var account accounts.Account
	found := false
	for _, a := range list {
		if a.ID == r.AccountID {
			account, found = a, true
			break
		}
	}
	if !found {
		enabled := accounts.Enabled(list)
		if len(enabled) == 0 {
			return r, ErrNoAccount
		}
		account = enabled[0]
	}
	if r.Status == "succeeded" {
		return r, nil
	}
	x, err := newXClient(ctx, v, mgr, account)
	if err != nil {
		return r, err
	}
	defer x.close()
	if err = verifyPublicCheckout(ctx, v, x, r, plan); err != nil {
		return r, err
	}
	if err = save(v, r); err != nil {
		return r, err
	}
	return r, nil
}

// verifyExisting classifies an existing session. It returns true when the
// caller may publish a replacement.
func verifyExisting(ctx context.Context, v *store.Store, x *xClient, existing *Record, plan Plan, requested bool) (bool, error) {
	err := verifyPublicCheckout(ctx, v, x, existing, plan)
	if err == nil {
		if existing.Status == "succeeded" || !requested {
			return false, nil
		}
		// A live session past its own window may be replaced on explicit
		// request once it is verified unpaid. A fresh one may not.
		if publicLinkFresh(existing, time.Now()) {
			return false, ErrPublicPaymentInProgress
		}
	}
	if errors.Is(err, ErrPublicPaymentInProgress) {
		return false, err
	}
	if err != nil && !errors.Is(err, ErrVerifyUnpaid) && !errors.Is(err, ErrPublicPaymentDeclined) {
		return false, err
	}
	// The session is expired/unpaid or explicitly declined: replacement is safe.
	return true, nil
}

func verifyNewLink(ctx context.Context, v *store.Store, x *xClient, r *Record, plan Plan) error {
	s, err := x.stripe(ctx)
	if err != nil {
		return err
	}
	defer s.close()
	page, err := s.page(ctx, r, true)
	if err != nil {
		return err
	}
	if err = page.guard(r, plan, true); err != nil {
		return err
	}
	return rememberVerifiedCheckout(v, r, plan, page)
}

func archiveReplaced(v *store.Store, existing *Record) error {
	raw, err := v.Get(recordKey(existing.RecipientID))
	if err != nil {
		return err
	}
	defer clear(raw)
	var saved Record
	if json.Unmarshal(raw, &saved) != nil || saved.SessionID != existing.SessionID {
		return ErrSessionMismatch
	}
	proof, err := json.Marshal(map[string]any{
		"previous":    json.RawMessage(raw),
		"replaced_at": time.Now().Unix(),
		"reason":      "explicit_link_regeneration",
	})
	if err != nil {
		return err
	}
	defer clear(proof)
	history := fmt.Sprintf("checkout-history:%s:%d", existing.RecipientID, time.Now().UnixNano())
	return v.Archive(recordKey(existing.RecipientID), history, raw, proof)
}

func planFor(v *store.Store, months int) (Plan, error) {
	catalog, err := ReadCatalog(v)
	if err != nil {
		return Plan{}, err
	}
	return catalog.PlanFor(months)
}

// ranked is one candidate for a new session, already resolved against the
// account's persisted creation slot.
type ranked struct {
	account accounts.Account
	wait    time.Duration
}

// rankAccounts orders the enabled pool for a new session. A pinned or
// previously bound account stays first so regeneration keeps the same X
// identity; the rest follow by ascending persisted wait, with accounts whose
// last probe failed pushed to the end. The caller walks this list when an
// account turns out to be broken, so a dead proxy or a revoked cookie costs one
// retry instead of the whole checkout.
func rankAccounts(v *store.Store, enabled []accounts.Account, existing *Record, pinned string, usable func(string) bool, now time.Time) ([]ranked, error) {
	preferred := pinned
	if preferred == "" && existing != nil {
		preferred = existing.AccountID
	}
	var head []ranked
	rest := make([]ranked, 0, len(enabled))
	for _, a := range enabled {
		w, err := CheckoutCreationWait(v, a.ID, now)
		if err != nil {
			return nil, err
		}
		if preferred != "" && a.ID == preferred {
			head = append(head, ranked{account: a, wait: w})
			continue
		}
		rest = append(rest, ranked{account: a, wait: w})
	}
	if pinned != "" && len(head) == 0 {
		return nil, errors.New("the account bound to this link is disabled or missing")
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if usable != nil {
			if ui, uj := usable(rest[i].account.ID), usable(rest[j].account.ID); ui != uj {
				return ui
			}
		}
		return rest[i].wait < rest[j].wait
	})
	return append(head, rest...), nil
}

// pickReadAccount spreads read-only requests across the pool.
func pickReadAccount(v *store.Store, enabled []accounts.Account) (accounts.Account, error) {
	var best accounts.Account
	var bestTime time.Time
	for i, a := range enabled {
		last := LastCreation(v, a.ID)
		if i == 0 || last.Before(bestTime) {
			best, bestTime = a, last
		}
	}
	if best.ID == "" {
		return accounts.Account{}, ErrNoAccount
	}
	return best, nil
}
