package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openTest creates a vault in a temp directory with an owner-only password file.
func openTest(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte("0123456789abcdef0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vault.db")
	v, err := Open(path, password, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, path
}

// backdate simulates records that have aged, which is the only way to exercise
// the retention window without waiting for it.
func backdate(t *testing.T, v *Vault, name string, when time.Time) {
	t.Helper()
	if _, err := v.db.Exec("UPDATE secrets SET created=? WHERE name=?", when.Unix(), name); err != nil {
		t.Fatal(err)
	}
}

func TestPrunePrefixesHonoursCutoffAndPrefix(t *testing.T) {
	v, _ := openTest(t)
	now := time.Now()
	old := now.AddDate(0, 0, -60)
	fresh := now.AddDate(0, 0, -1)
	for _, name := range []string{
		"checkout-verification:cs_old",
		"checkout-verification:cs_fresh",
		"checkout-completion:cs_old",
		"redemption:old",
		"checkout:old",
	} {
		if err := v.Put(name, []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	backdate(t, v, "checkout-verification:cs_old", old)
	backdate(t, v, "checkout-verification:cs_fresh", fresh)
	backdate(t, v, "checkout-completion:cs_old", old)
	backdate(t, v, "redemption:old", old)
	backdate(t, v, "checkout:old", old)

	removed, err := v.PrunePrefixes(now.AddDate(0, 0, -30).Unix(), "checkout-verification:")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	for _, name := range []string{"checkout-verification:cs_fresh", "checkout-completion:cs_old", "redemption:old", "checkout:old"} {
		if _, err := v.Get(name); err != nil {
			t.Errorf("%s should have survived: %v", name, err)
		}
	}
	if _, err := v.Get("checkout-verification:cs_old"); err == nil {
		t.Error("expired verification record should have been pruned")
	}
}

// A prefix without the separator would let "checkout-verification:" reach into
// an unrelated family, so it is rejected rather than silently widening.
func TestPrunePrefixesRejectsBarePrefix(t *testing.T) {
	v, _ := openTest(t)
	if err := v.Put("checkout-verification:x", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.PrunePrefixes(time.Now().Unix(), "checkout-verification"); err == nil {
		t.Fatal("expected a bare prefix to be rejected")
	}
	if _, err := v.Get("checkout-verification:x"); err != nil {
		t.Errorf("record should have survived a rejected prune: %v", err)
	}
}

// Rewriting a record must not restart its retention clock, or an often-updated
// record would never age out.
func TestPutPreservesCreationTime(t *testing.T) {
	v, _ := openTest(t)
	if err := v.Put("checkout-verification:x", []byte("first")); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 0, -100)
	backdate(t, v, "checkout-verification:x", when)
	if err := v.Put("checkout-verification:x", []byte("second")); err != nil {
		t.Fatal(err)
	}
	var created int64
	if err := v.db.QueryRow("SELECT created FROM secrets WHERE name=?", "checkout-verification:x").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != when.Unix() {
		t.Errorf("created = %d, want %d", created, when.Unix())
	}
}

func TestCountPrefixesSeparatesFamilies(t *testing.T) {
	v, _ := openTest(t)
	for i := 0; i < 3; i++ {
		if err := v.Put("redemption:"+string(rune('a'+i)), []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Put("checkout:x", []byte("value")); err != nil {
		t.Fatal(err)
	}
	usage, err := v.CountPrefixes("redemption:", "checkout:")
	if err != nil {
		t.Fatal(err)
	}
	if usage["redemption:"].Records != 3 {
		t.Errorf("redemption records = %d, want 3", usage["redemption:"].Records)
	}
	if usage["checkout:"].Records != 1 {
		t.Errorf("checkout records = %d, want 1", usage["checkout:"].Records)
	}
	if usage["redemption:"].Bytes == 0 {
		t.Error("redemption bytes should include the encrypted payloads")
	}
}

// A vault created before retention existed must gain the column and start every
// record's clock at the upgrade, so an upgrade cannot delete anything. The
// fixture has to remove the column to reach that path — a freshly created vault
// already has it.
func TestMigrateAddsCreatedColumn(t *testing.T) {
	v, path := openTest(t)
	if _, err := v.db.Exec("ALTER TABLE secrets DROP COLUMN created"); err != nil {
		t.Fatalf("could not simulate a pre-retention vault: %v", err)
	}
	if _, err := v.db.Exec("INSERT INTO secrets(name,payload) VALUES ('legacy', 'x')"); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening runs the migration path rather than the create path.
	reopened, err := Open(path, filepath.Join(filepath.Dir(path), "password"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var created int64
	if err := reopened.db.QueryRow("SELECT created FROM secrets WHERE name='legacy'").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created < time.Now().Add(-time.Hour).Unix() {
		t.Errorf("legacy record was stamped with %d; it should start its clock at the upgrade", created)
	}
}
