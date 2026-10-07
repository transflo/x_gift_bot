package checkout

import "regexp"

// X's public gifting product identifiers as discovered from the x.com checkout
// page. They are merchant-side identifiers published by X, not operator
// secrets, so they can be seeded when the record is missing.
const (
	DefaultXMerchant   = "acct_1Ika5JA3KZ32dPo1"
	DefaultXCurrency   = "bdt"
	DefaultXProduct3Mo = "prod_TJXJtpzqCpI36N"
	DefaultXProduct6Mo = "prod_TJXKKNJwZJIhCM"
)

var stripeKeyPattern = regexp.MustCompile(`^pk_live_[A-Za-z0-9]+$`)

// ValidStripeKey reports whether key is a Stripe live publishable key. Only
// publishable keys are ever accepted; the server never handles secret keys.
func ValidStripeKey(key string) bool {
	return stripeKeyPattern.MatchString(key)
}

// DefaultAPIAuthorization is the bearer x.com serves to its own web client;
// the user agent matches a desktop browser because X rejects unknown clients.
const (
	DefaultAPIAuthorization = "Bearer AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
	DefaultAPIUserAgent     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

// DefaultCatalog is X Premium's published 3/6 month gifting catalog.
func DefaultCatalog() Catalog {
	return Catalog{
		Merchant: DefaultXMerchant,
		Currency: DefaultXCurrency,
		Plans: []CatalogPlan{
			{Months: 3, Amount: 259900, Product: DefaultXProduct3Mo},
			{Months: 6, Amount: 479900, Product: DefaultXProduct6Mo},
		},
	}
}
