package store

import (
	"path/filepath"
	"testing"
	"time"
)

// openTest creates a store in a temp directory.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// backdate simulates records that have aged, which is the only way to exercise
// the retention window without waiting for it.
func backdate(t *testing.T, s *Store, name string, when time.Time) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE records SET created=? WHERE name=?", when.Unix(), name); err != nil {
		t.Fatal(err)
	}
}

func TestPrunePrefixesHonoursCutoffAndPrefix(t *testing.T) {
	s := openTest(t)
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
		if err := s.Put(name, []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	backdate(t, s, "checkout-verification:cs_old", old)
	backdate(t, s, "checkout-verification:cs_fresh", fresh)
	backdate(t, s, "checkout-completion:cs_old", old)
	backdate(t, s, "redemption:old", old)
	backdate(t, s, "checkout:old", old)

	removed, err := s.PrunePrefixes(now.AddDate(0, 0, -30).Unix(), "checkout-verification:")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	for _, name := range []string{"checkout-verification:cs_fresh", "checkout-completion:cs_old", "redemption:old", "checkout:old"} {
		if _, err := s.Get(name); err != nil {
			t.Errorf("%s should have survived: %v", name, err)
		}
	}
	if _, err := s.Get("checkout-verification:cs_old"); err == nil {
		t.Error("expired verification record should have been pruned")
	}
}

// A prefix without the separator would let "checkout-verification:" reach into
// an unrelated family, so it is rejected rather than silently widening.
func TestPrunePrefixesRejectsBarePrefix(t *testing.T) {
	s := openTest(t)
	if err := s.Put("checkout-verification:x", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrunePrefixes(time.Now().Unix(), "checkout-verification"); err == nil {
		t.Fatal("expected a bare prefix to be rejected")
	}
	if _, err := s.Get("checkout-verification:x"); err != nil {
		t.Errorf("record should have survived a rejected prune: %v", err)
	}
}

// Rewriting a record must not restart its retention clock, or an often-updated
// record would never age out.
func TestPutPreservesCreationTime(t *testing.T) {
	s := openTest(t)
	if err := s.Put("checkout-verification:x", []byte("first")); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 0, -100)
	backdate(t, s, "checkout-verification:x", when)
	if err := s.Put("checkout-verification:x", []byte("second")); err != nil {
		t.Fatal(err)
	}
	var created int64
	if err := s.db.QueryRow("SELECT created FROM records WHERE name=?", "checkout-verification:x").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != when.Unix() {
		t.Errorf("created = %d, want %d", created, when.Unix())
	}
}

func TestCountPrefixesSeparatesFamilies(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 3; i++ {
		if err := s.Put("redemption:"+string(rune('a'+i)), []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put("checkout:x", []byte("value")); err != nil {
		t.Fatal(err)
	}
	usage, err := s.CountPrefixes("redemption:", "checkout:")
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
		t.Error("redemption bytes should include the payloads")
	}
}
