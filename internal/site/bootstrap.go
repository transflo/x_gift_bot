package site

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xgift/internal/checkout"
	"xgift/internal/store"
)

// adminSlugPattern matches the minted admin path: 20 lowercase hex characters.
var adminSlugPattern = regexp.MustCompile(`^[0-9a-f]{20}$`)

// adminPath returns the random URL segment the admin UI lives under. It is
// minted on first start and kept in the data directory so the panel is not
// reachable at a path an automated scanner can guess. The password remains the
// real control; this only keeps the login form out of drive-by reach.
func adminPath(dir string) (string, error) {
	path := filepath.Join(dir, "admin-path")
	raw, err := os.ReadFile(path)
	if err == nil {
		slug := strings.TrimSpace(string(raw))
		if !adminSlugPattern.MatchString(slug) {
			return "", errors.New("admin-path file is not a valid admin path; delete it to mint a new one")
		}
		return slug, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	secret := make([]byte, 10)
	if _, err = rand.Read(secret); err != nil {
		return "", err
	}
	slug := hex.EncodeToString(secret)
	if err = os.WriteFile(path, []byte(slug+"\n"), 0600); err != nil {
		return "", err
	}
	return slug, nil
}

// adminPassword prefers the value from the environment; the file form stays
// supported for deployments created by the setup wizard.
func adminPassword(dir string) ([]byte, error) {
	if fromEnv := os.Getenv("XGIFT_ADMIN_PASSWORD"); fromEnv != "" {
		return []byte(strings.TrimSpace(fromEnv)), nil
	}
	path := os.Getenv("XGIFT_ADMIN_PASSWORD_FILE")
	if path == "" {
		path = filepath.Join(dir, "admin-password")
	}
	raw, err := privateFile(path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(raw))), nil
}

// seedDefaults writes the records that have published defaults, so a checkout
// can succeed before an operator configures anything. Records that already
// exist are left alone, and the account pool is deliberately not seeded — it
// only holds operator secrets and the admin UI is the intended place to add it.
func seedDefaults(v *store.Store) error {
	auth, err := json.Marshal(map[string]string{
		"Authorization": checkout.DefaultAPIAuthorization,
		"UserAgent":     checkout.DefaultAPIUserAgent,
	})
	if err != nil {
		return err
	}
	catalog, err := json.Marshal(checkout.DefaultCatalog())
	if err != nil {
		return err
	}
	seeds := map[string][]byte{"api-auth": auth, "catalog": catalog}
	if key := strings.TrimSpace(os.Getenv("XGIFT_STRIPE_KEY")); key != "" {
		if !checkout.ValidStripeKey(key) {
			return errors.New("XGIFT_STRIPE_KEY must be the pk_live_ publishable key")
		}
		seeds["stripe-key"] = []byte(key)
	}
	for name, raw := range seeds {
		if _, err = v.Get(name); err == nil {
			continue
		}
		if err = v.Put(name, raw); err != nil {
			return err
		}
		log.Printf("已写入默认配置：%s", name)
	}
	return nil
}
