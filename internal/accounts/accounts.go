// Package accounts manages the pool of X accounts and their Shadowsocks
// proxies. Every account is bound to exactly one proxy, and only
// Shadowsocks outbounds are accepted.
package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

var (
	idPattern   = regexp.MustCompile(`^[a-f0-9]{16}$`)
	ssMethods   = regexp.MustCompile(`^[A-Za-z0-9._-]{3,64}$`)
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,253}$`)
)

// ErrNoEnabledAccount reports an empty or fully disabled pool.
var ErrNoEnabledAccount = errors.New("no enabled X account is configured")

// Proxy describes exactly one Shadowsocks outbound. The JSON field names match
// sing-box option names so the account record can be translated into an
// embedded sing-box configuration without loss.
type Proxy struct {
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
}

// Account is one X identity plus the proxy it must use for every request.
// The auth_token/ct0 pair is the minimum cookie set required by x.com.
type Account struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	AuthToken string `json:"auth_token"`
	CT0       string `json:"ct0"`
	Enabled   bool   `json:"enabled"`
	Proxy     Proxy  `json:"proxy"`
}

// NewID returns a fresh random account identifier. IDs are stable references;
// labels are display-only and may repeat.
func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func cleanSecret(name, value string, max int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > max || strings.ContainsAny(value, "\r\n\x00;") {
		return fmt.Errorf("%s contains invalid characters or is too long", name)
	}
	return nil
}

// Validate checks one account record without touching the network.
func (a *Account) Validate() error {
	if a.ID == "" {
		id, err := NewID()
		if err != nil {
			return errors.New("could not allocate an account id")
		}
		a.ID = id
	}
	if !idPattern.MatchString(a.ID) {
		return errors.New("account id must be 16 lowercase hex characters")
	}
	if len(a.Label) > 64 || strings.ContainsAny(a.Label, "\r\n\x00") {
		return errors.New("account label must be at most 64 printable characters")
	}
	if err := cleanSecret("auth_token", a.AuthToken, 8192); err != nil {
		return err
	}
	if err := cleanSecret("ct0", a.CT0, 8192); err != nil {
		return err
	}
	return a.Proxy.Validate()
}

// Validate accepts only a real Shadowsocks endpoint. Direct connections are
// deliberately not supported: every account must leave through its own node.
func (p *Proxy) Validate() error {
	if p.Type != "shadowsocks" {
		return errors.New("only shadowsocks proxies are supported")
	}
	if !hostPattern.MatchString(p.Server) {
		return errors.New("proxy server is invalid")
	}
	if ip := net.ParseIP(p.Server); ip == nil && strings.Contains(p.Server, ":") {
		return errors.New("proxy server must not embed a port")
	}
	if p.ServerPort < 1 || p.ServerPort > 65535 {
		return errors.New("proxy server_port must be 1..65535")
	}
	if !ssMethods.MatchString(p.Method) {
		return errors.New("proxy method is invalid")
	}
	if err := cleanSecret("proxy password", p.Password, 1024); err != nil {
		return err
	}
	return nil
}

// Outbound renders the sing-box outbound object for this proxy.
func (p Proxy) Outbound() (json.RawMessage, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"type":        "shadowsocks",
		"tag":         "account",
		"server":      p.Server,
		"server_port": p.ServerPort,
		"method":      p.Method,
		"password":    p.Password,
	})
}

// Fingerprint is a non-secret digest used to detect proxy edits without
// exposing credentials in logs or keys.
func (p Proxy) Fingerprint() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%s", p.Type, p.Server, p.ServerPort, p.Method, p.Password)))
	return hex.EncodeToString(sum[:8])
}

// Parse accepts either a bare JSON array of accounts or an object with an
// "accounts" array. It validates every record and rejects duplicate IDs.
func Parse(raw []byte) ([]Account, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, errors.New("accounts record is empty")
	}
	var list []Account
	if raw[0] == '{' {
		var envelope struct {
			Accounts []Account `json:"accounts"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, errors.New("invalid accounts JSON")
		}
		list = envelope.Accounts
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("invalid accounts JSON")
	}
	if len(list) == 0 {
		return nil, errors.New("at least one X account is required")
	}
	if len(list) > 64 {
		return nil, errors.New("at most 64 X accounts are supported")
	}
	seen := map[string]bool{}
	for i := range list {
		if err := list[i].Validate(); err != nil {
			return nil, fmt.Errorf("account %d: %w", i+1, err)
		}
		if seen[list[i].ID] {
			return nil, fmt.Errorf("account %d: duplicate id", i+1)
		}
		seen[list[i].ID] = true
	}
	return list, nil
}

// Enabled returns only the accounts eligible to serve traffic.
func Enabled(list []Account) []Account {
	out := make([]Account, 0, len(list))
	for _, a := range list {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out
}
