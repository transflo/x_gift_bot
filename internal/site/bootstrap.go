package site

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xgift/internal/checkout"
	"xgift/internal/vault"
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

// vaultPasswordPath is the file the vault is encrypted with, defaulting to the
// data directory so a container volume is self-contained.
func vaultPasswordPath(dir string) string {
	if path := os.Getenv("XGIFT_PASSWORD_FILE"); path != "" {
		return path
	}
	return filepath.Join(dir, "vault-password")
}

// openVault opens the encrypted store, minting a password file on first start
// so a fresh data volume comes up without the setup wizard. An operator that
// already ran the wizard keeps using its file untouched.
func openVault(dir string) (*vault.Vault, error) {
	passwordFile := vaultPasswordPath(dir)
	if _, err := os.Lstat(passwordFile); errors.Is(err, os.ErrNotExist) {
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(passwordFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if _, err = f.WriteString(base64.RawURLEncoding.EncodeToString(secret) + "\n"); err != nil {
			f.Close()
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		log.Printf("已生成保管库密码文件 %s", passwordFile)
		log.Printf("请立即备份该文件：丢失后保管库中的所有加密记录都无法恢复。")
	}
	dbPath := filepath.Join(dir, "vault.db")
	_, statErr := os.Stat(dbPath)
	return vault.Open(dbPath, passwordFile, errors.Is(statErr, os.ErrNotExist))
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
func seedDefaults(v *vault.Vault) error {
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

// vaultBackup hands the vault password to the admin UI so the operator can
// back it up. This grants no new access — an admin session already decrypts
// every record — and the UI keeps it masked until explicitly revealed. The
// file is re-read per request rather than held in memory.
func (s *server) vaultBackup(w http.ResponseWriter, r *http.Request) {
	path := vaultPasswordPath(s.dir)
	raw, err := privateFile(path)
	if err != nil {
		message(w, 503, "无法读取保管库密码文件。")
		return
	}
	defer clear(raw)
	reply(w, http.StatusOK, map[string]string{
		"path":     path,
		"password": strings.TrimSpace(string(raw)),
	})
}
