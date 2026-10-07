package checkout

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testRecord() *Record {
	return &Record{
		Username:    "alice",
		RecipientID: "12345",
		Months:      3,
		Amount:      259900,
		Currency:    "BDT",
		ProductID:   "prod_TEST3MO",
		SessionID:   "cs_live_TEST123",
		URL:         "https://checkout.stripe.com/c/pay/cs_live_TEST123",
		Status:      "created",
	}
}

func testPlan() Plan {
	return Plan{Months: 3, Minor: 259900, ProductID: "prod_TEST3MO", Merchant: "acct_TESTMERCHANT", Currency: "bdt"}
}

func validPageJSON(sessionID string) string {
	return fmt.Sprintf(`{
  "session_id": %q,
  "currency": "bdt",
  "mode": "payment",
  "livemode": true,
  "status": "open",
  "payment_status": "unpaid",
  "init_checksum": "checksum",
  "success_url": "https://x.com/alice/gift-premium/success",
  "cancel_url": "https://x.com/alice/gift-premium",
  "account_settings": {"account_id": "acct_TESTMERCHANT"},
  "total_summary": {"due": 259900, "subtotal": 259900, "total": 259900},
  "line_item_group": {
    "currency": "bdt", "due": 259900, "subtotal": 259900, "total": 259900,
    "line_items": [{
      "name": "Premium Gift - 3 months", "quantity": 1, "subtotal": 259900, "total": 259900,
      "price": {"currency": "bdt", "type": "one_time", "unit_amount": 259900,
        "product": {"id": "prod_TEST3MO", "name": "Premium Gift - 3 months", "livemode": true}}
    }]
  },
  "payment_intent": null
}`, sessionID)
}

func TestCheckoutLinkValidation(t *testing.T) {
	r := testRecord()
	if CheckoutLink(r) == "" {
		t.Fatal("valid link rejected")
	}
	r.URL = "https://evil.example/pay/" + r.SessionID
	if CheckoutLink(r) != "" {
		t.Fatal("foreign host accepted")
	}
	r = testRecord()
	r.SessionID = "pi_live_TEST"
	if CheckoutLink(r) != "" {
		t.Fatal("non-session id accepted")
	}
}

func TestPaymentPageGuard(t *testing.T) {
	r, plan := testRecord(), testPlan()
	var page paymentPage
	if err := json.Unmarshal([]byte(validPageJSON(r.SessionID)), &page); err != nil {
		t.Fatal(err)
	}
	if err := page.guard(r, plan, true); err != nil {
		t.Fatalf("valid page rejected: %v", err)
	}
	if !page.IntentPresent || !page.IntentNull {
		t.Fatal("null payment intent flags not recorded")
	}
	bad := strings.Replace(validPageJSON(r.SessionID), `"currency": "bdt"`, `"currency": "USD"`, 1)
	page = paymentPage{}
	if err := json.Unmarshal([]byte(bad), &page); err != nil {
		t.Fatal(err)
	}
	if err := page.guard(r, plan, true); err == nil {
		t.Fatal("currency mismatch accepted")
	}
	active := strings.Replace(validPageJSON(r.SessionID), `"payment_intent": null`, `"payment_intent": {"id":"pi_1","status":"requires_payment_method","currency":"bdt","amount":259900,"amount_received":0,"amount_capturable":0}`, 1)
	page = paymentPage{}
	if err := json.Unmarshal([]byte(active), &page); err != nil {
		t.Fatal(err)
	}
	if err := page.guard(r, plan, true); err == nil {
		t.Fatal("non-null intent accepted before publishing")
	}
	if err := page.guard(r, plan, false); err != nil {
		t.Fatalf("idle intent rejected after publication: %v", err)
	}
	if !publicIntentIdle(&page) {
		t.Fatal("idle intent not classified as idle")
	}
}

func TestInactiveCheckoutClassification(t *testing.T) {
	if !inactiveCheckout(&stripeError{Code: "resource_missing", HTTP: 404}) {
		t.Fatal("resource_missing not classified as inactive")
	}
	if inactiveCheckout(errors.New("boom")) {
		t.Fatal("generic error misclassified")
	}
	if inactiveCheckout(&stripeError{HTTP: 500}) {
		t.Fatal("server error misclassified")
	}
}

func TestSessionPattern(t *testing.T) {
	if !sessionURL("https://checkout.stripe.com/c/pay/cs_live_ABC123", "cs_live_ABC123") {
		t.Fatal("valid session URL rejected")
	}
	if sessionURL("https://checkout.stripe.com/c/pay/cs_live_ABC123?x=1", "cs_live_ABC123") {
		t.Fatal("session URL with query accepted")
	}
	if upperCurrency("bdt") != "BDT" {
		t.Fatal("currency not uppercased")
	}
}
