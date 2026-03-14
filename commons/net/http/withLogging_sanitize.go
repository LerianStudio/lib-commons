package http

import (
	"net/url"
	"strings"
)

// sanitizeReferer strips query parameters and userinfo from a Referer header value
// before it is written into logs, preventing credential/token leakage.
func sanitizeReferer(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "-"
	}

	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String()
}

func sanitizeLogValue(raw string) string {
	replacer := strings.NewReplacer("\r", "", "\n", "", "\x00", "")
	return replacer.Replace(raw)
}
