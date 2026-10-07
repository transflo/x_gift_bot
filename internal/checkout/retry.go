package checkout

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const maxAttempts = 3

type temporaryError struct {
	cause error
	delay time.Duration
}

func (e *temporaryError) Error() string { return e.cause.Error() }
func (e *temporaryError) Unwrap() error { return e.cause }
func temporary(err error) error         { return &temporaryError{cause: err} }

// transportFailure marks a request that never reached X because the account's
// proxy could not carry it.
type transportFailure struct{ cause error }

func (e *transportFailure) Error() string { return e.cause.Error() }
func (e *transportFailure) Unwrap() error { return e.cause }

// statusFailure carries the HTTP status X returned, so callers can tell an
// account problem from an X problem.
type statusFailure struct {
	cause  error
	status int
}

func (e *statusFailure) Error() string { return e.cause.Error() }
func (e *statusFailure) Unwrap() error { return e.cause }

// accountFault reports whether a failure points at one account and its proxy
// rather than at X itself, which is what makes trying the next account in the
// pool worthwhile:
//
//   - a transport failure means the Shadowsocks tunnel is down;
//   - 401/403 means the cookie was revoked or the account is suspended;
//   - 404 means the account stopped resolving;
//   - 429 means that account is rate limited, and another exit IP may not be.
//
// A 5xx is deliberately excluded: it points at X, and the ordinary backoff
// retry already handles it.
func accountFault(err error) bool {
	var transport *transportFailure
	if errors.As(err, &transport) {
		return true
	}
	var status *statusFailure
	if errors.As(err, &status) {
		switch status.status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests:
			return true
		}
	}
	return false
}

func httpFailure(err error, status int, retryAfter string) error {
	if status != 429 && (status < 500 || status > 599) {
		return err
	}
	delay := time.Duration(0)
	if seconds, e := strconv.Atoi(retryAfter); e == nil && seconds > 0 {
		if seconds > 60 {
			return err
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, e := http.ParseTime(retryAfter); e == nil {
		delay = time.Until(date)
		if delay > 60*time.Second {
			return err
		}
	}
	return &temporaryError{cause: err, delay: delay}
}
func waitRetry(ctx context.Context, err error, attempt int) error {
	var retry *temporaryError
	if attempt >= maxAttempts || !errors.As(err, &retry) {
		return err
	}
	delay := time.Duration(attempt*2) * time.Second
	if retry.delay > delay {
		delay = retry.delay
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return err
	}
	progress(ctx, -1, fmt.Sprintf("连接暂时不稳定，正在自动恢复（第 %d/%d 次）…", attempt+1, maxAttempts))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Only called at explicitly safe operations. Never wrap payment confirmation.
func retrySafe(ctx context.Context, call func() error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil {
			return nil
		}
		if stop := waitRetry(ctx, err, attempt); stop != nil {
			return stop
		}
	}
}
