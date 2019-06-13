package authentication

import (
	"github.com/ovh/cds/engine/api/cache"
	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/log"
)

var _XSRFTokenDuration = 60 * 60 * 24 * 7 // 1 Week

// StoreXSRFToken generate and store a CSRF token for a given access_token
func StoreXSRFToken(store cache.Store, sessionID string) string {
	log.Debug("authentication.StoreXSRFToken")
	var xsrfToken = sdk.UUID()
	var k = cache.Key("token", "xsrf", sessionID)
	store.SetWithTTL(k, &xsrfToken, _XSRFTokenDuration)
	return xsrfToken
}

// CheckXSRFToken checks a value "xsrfToken" against the access token CSRF generated by the API
func CheckXSRFToken(store cache.Store, sessionID, xsrfToken string) bool {
	log.Debug("authentication.CheckXSRFToken")
	var expectedXSRFfToken string
	var k = cache.Key("token", "xsrf", sessionID)
	if store.Get(k, &expectedXSRFfToken) {
		return expectedXSRFfToken == xsrfToken
	}
	return false
}
