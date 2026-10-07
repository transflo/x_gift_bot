package accounts

import (
	"encoding/json"
	"strings"
	"testing"
)

func validProxy() Proxy {
	return Proxy{Type: "shadowsocks", Server: "1.2.3.4", ServerPort: 8388, Method: "aes-256-gcm", Password: "secret"}
}

func validAccount() Account {
	return Account{ID: "0123456789abcdef", Label: "主账号", AuthToken: "token", CT0: "csrf", Enabled: true, Proxy: validProxy()}
}

func TestParseAccounts(t *testing.T) {
	list := []Account{validAccount()}
	raw, _ := json.Marshal(list)
	parsed, err := Parse(raw)
	if err != nil || len(parsed) != 1 || parsed[0].ID != "0123456789abcdef" {
		t.Fatalf("valid account rejected: %v", err)
	}
	envelope, _ := json.Marshal(map[string]any{"accounts": list})
	if _, err = Parse(envelope); err != nil {
		t.Fatalf("envelope form rejected: %v", err)
	}
}

func TestParseRejectsNonShadowsocks(t *testing.T) {
	a := validAccount()
	a.Proxy.Type = "vless"
	raw, _ := json.Marshal([]Account{a})
	if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "shadowsocks") {
		t.Fatalf("non-shadowsocks proxy accepted: %v", err)
	}
}

func TestParseRejectsDuplicates(t *testing.T) {
	a := validAccount()
	raw, _ := json.Marshal([]Account{a, a})
	if _, err := Parse(raw); err == nil {
		t.Fatal("duplicate ids accepted")
	}
}

func TestValidateRequiresCookies(t *testing.T) {
	a := validAccount()
	a.AuthToken = ""
	raw, _ := json.Marshal([]Account{a})
	if _, err := Parse(raw); err == nil {
		t.Fatal("missing auth_token accepted")
	}
}

func TestProxyOutbound(t *testing.T) {
	out, err := validProxy().Outbound()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["type"] != "shadowsocks" || fields["server_port"].(float64) != 8388 {
		t.Fatalf("unexpected outbound: %s", out)
	}
}

func TestSanitizeHidesSecrets(t *testing.T) {
	view := Sanitize(validAccount())
	encoded, _ := json.Marshal(view)
	text := string(encoded)
	for _, secret := range []string{"token", "csrf", "secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("sanitized view leaks %q: %s", secret, text)
		}
	}
}
