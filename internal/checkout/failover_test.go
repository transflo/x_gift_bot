package checkout

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"xgift/internal/accounts"
	"xgift/internal/store"
)

// accountFault decides whether a checkout failure is worth retrying on another
// account. Getting this wrong either strands a working pool behind one dead
// proxy, or burns the whole pool on an X-wide outage.
func TestAccountFaultClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"proxy tunnel down", temporary(&transportFailure{cause: errors.New("dial tcp: refused")}), true},
		{"cookie revoked", xHTTPFailure(errors.New("X request failed"), http.StatusUnauthorized, "", true), true},
		{"account suspended", xHTTPFailure(errors.New("X request failed"), http.StatusForbidden, "", true), true},
		{"account no longer resolves", xHTTPFailure(errors.New("X request failed"), http.StatusNotFound, "", true), true},
		{"account rate limited", xHTTPFailure(errors.New("X request failed"), http.StatusTooManyRequests, "", true), true},
		{"X is down", xHTTPFailure(errors.New("X 503"), http.StatusServiceUnavailable, "", true), false},
		{"our own validation", errors.New("recipient identity changed"), false},
	}
	for _, tc := range cases {
		if got := accountFault(tc.err); got != tc.want {
			t.Errorf("%s: accountFault=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func testPool(t *testing.T) (*store.Store, []accounts.Account) {
	t.Helper()
	v, err := store.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	list := []accounts.Account{}
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		list = append(list, accounts.Account{
			ID: id, Label: id, Enabled: true, AuthToken: "token", CT0: "ct0",
			Proxy: accounts.Proxy{Type: "shadowsocks", Server: "127.0.0.1", ServerPort: 8388, Method: "aes-256-gcm", Password: "secret"},
		})
	}
	return v, list
}

func TestRankAccountsKeepsBoundAccountFirst(t *testing.T) {
	v, list := testPool(t)
	ranked, err := rankAccounts(v, list, &Record{AccountID: "ccc"}, "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].account.ID != "ccc" {
		t.Fatalf("first=%s, want the previously bound account ccc", ranked[0].account.ID)
	}
	if len(ranked) != 3 {
		t.Fatalf("got %d candidates, want every enabled account offered", len(ranked))
	}
}

func TestRankAccountsFallsBackWhenBoundAccountRemoved(t *testing.T) {
	v, list := testPool(t)
	ranked, err := rankAccounts(v, list, &Record{AccountID: "gone"}, "", nil, time.Now())
	if err != nil {
		t.Fatalf("a removed account must fall back to the pool, got %v", err)
	}
	if len(ranked) != 3 {
		t.Fatalf("got %d candidates, want the whole pool", len(ranked))
	}
}

func TestRankAccountsRejectsMissingPinnedAccount(t *testing.T) {
	v, list := testPool(t)
	if _, err := rankAccounts(v, list, nil, "gone", nil, time.Now()); err == nil {
		t.Fatal("a pinned account that is disabled or missing must be rejected, not substituted")
	}
}

func TestRankAccountsPushesUnusableLast(t *testing.T) {
	v, list := testPool(t)
	usable := func(id string) bool { return id != "aaa" }
	ranked, err := rankAccounts(v, list, nil, "", usable, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].account.ID == "aaa" {
		t.Fatal("an account whose last probes failed must not be offered first")
	}
	if last := ranked[len(ranked)-1].account.ID; last != "aaa" {
		t.Fatalf("last=%s, want the failing account aaa to be the last resort", last)
	}
}
