package http

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	constant "github.com/LerianStudio/lib-commons/v4/commons/constants"

	"github.com/gofiber/fiber/v2"
)

// BasicAuthFunc represents a func which returns if a username and password was authenticated or not.
// It returns true if authenticated, and false when not authenticated.
type BasicAuthFunc func(username, password string) bool

// FixedBasicAuthFunc is a fixed username and password to use as BasicAuthFunc.
func FixedBasicAuthFunc(username, password string) BasicAuthFunc {
	return func(user, pass string) bool {
		if subtle.ConstantTimeCompare([]byte(user), []byte(username)) == 1 && subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1 {
			return true
		}

		return false
	}
}

// WithBasicAuth creates a basic authentication middleware.
func WithBasicAuth(f BasicAuthFunc, realm string) fiber.Handler {
	safeRealm := sanitizeBasicAuthRealm(realm)

	return func(c *fiber.Ctx) error {
		if f == nil {
			return unauthorizedResponse(c, safeRealm)
		}

		auth := c.Get(constant.Authorization)
		if auth == "" {
			return unauthorizedResponse(c, safeRealm)
		}

		parts := strings.SplitN(auth, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], constant.Basic) {
			return unauthorizedResponse(c, safeRealm)
		}

		cred, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return unauthorizedResponse(c, safeRealm)
		}

		pair := strings.SplitN(string(cred), ":", 2)
		if len(pair) != 2 {
			return unauthorizedResponse(c, safeRealm)
		}

		if f(pair[0], pair[1]) {
			return c.Next()
		}

		return unauthorizedResponse(c, safeRealm)
	}
}

// sanitizeBasicAuthRealm strips CR, LF, and double-quote characters from the realm string.
func sanitizeBasicAuthRealm(realm string) string {
	realm = strings.TrimSpace(realm)

	return strings.NewReplacer("\r", "", "\n", "", "\"", "").Replace(realm)
}

// unauthorizedResponse sends a 401 response with a WWW-Authenticate header.
func unauthorizedResponse(c *fiber.Ctx, realm string) error {
	c.Set(constant.WWWAuthenticate, `Basic realm="`+realm+`"`)

	return RespondError(c, http.StatusUnauthorized, "invalid_credentials", "The provided credentials are invalid. Please provide valid credentials and try again.")
}
