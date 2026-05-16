package http

import (
	"encoding/json"
	stdlog "log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/gofiber/fiber/v2"
)

// maxObfuscationDepth limits recursion depth when obfuscating nested JSON structures
// to prevent stack overflow on deeply nested or malicious payloads.
const maxObfuscationDepth = 32

// logObfuscationDisabled caches the LOG_OBFUSCATION_DISABLED env var at init time
// to avoid repeated syscalls on every request.
var logObfuscationDisabled = os.Getenv("LOG_OBFUSCATION_DISABLED") == "true"

func init() {
	if logObfuscationDisabled {
		stdlog.Println("[WARN] LOG_OBFUSCATION_DISABLED is set to true. Sensitive data may appear in logs. Ensure this is not enabled in production.")
	}
}

// RequestInfo is a struct design to store http access log data.
//
// Deprecated: use RequestInfo from github.com/LerianStudio/lib-observability/middleware.
type RequestInfo struct {
	Method        string
	Username      string
	URI           string
	Referer       string
	RemoteAddress string
	Status        int
	Date          time.Time
	Duration      time.Duration
	UserAgent     string
	TraceID       string
	Protocol      string
	Size          int
	Body          string
}

// ResponseMetricsWrapper is a Wrapper responsible for collecting the response data such as status code and size.
//
// Deprecated: use ResponseMetricsWrapper from github.com/LerianStudio/lib-observability/middleware.
type ResponseMetricsWrapper struct {
	Context    *fiber.Ctx
	StatusCode int
	Size       int
}

// NewRequestInfo creates an instance of RequestInfo.
// The obfuscationDisabled parameter controls whether sensitive fields in the
// request body are obfuscated. Pass the middleware's effective setting (which
// combines the global LOG_OBFUSCATION_DISABLED env var with per-middleware
// overrides via WithObfuscationDisabled) to honour per-middleware configuration.
//
// Deprecated: use NewRequestInfo from github.com/LerianStudio/lib-observability/middleware.
func NewRequestInfo(c *fiber.Ctx, obfuscationDisabled bool) *RequestInfo {
	if c == nil {
		return &RequestInfo{Date: time.Now().UTC()}
	}

	username, referer := "-", "-"
	rawURL := string(c.Request().URI().FullURI())

	parsedURL, err := url.Parse(rawURL)
	if err == nil && parsedURL.User != nil {
		if name := parsedURL.User.Username(); name != "" {
			username = name
		}
	}

	if c.Get(cn.HeaderReferer) != "" {
		referer = sanitizeReferer(c.Get(cn.HeaderReferer))
	}

	body := ""

	if c.Request().Header.ContentLength() > 0 {
		bodyBytes := c.Body()
		if !obfuscationDisabled {
			body = getBodyObfuscatedString(c, bodyBytes)
		} else {
			body = string(bodyBytes)
		}
	}

	return &RequestInfo{
		TraceID:       c.Get(cn.HeaderID),
		Method:        c.Method(),
		URI:           sanitizeURL(c.OriginalURL()),
		Username:      username,
		Referer:       referer,
		UserAgent:     sanitizeLogValue(c.Get(cn.HeaderUserAgent)),
		RemoteAddress: c.IP(),
		Protocol:      c.Protocol(),
		Date:          time.Now().UTC(),
		Body:          body,
	}
}

// CLFString produces a log entry format similar to Common Log Format (CLF)
// Ref: https://httpd.apache.org/docs/trunk/logs.html#common
//
// Deprecated: use RequestInfo.CLFString from github.com/LerianStudio/lib-observability/middleware.
func (r *RequestInfo) CLFString() string {
	return strings.Join([]string{
		sanitizeLogValue(r.RemoteAddress),
		"-",
		sanitizeLogValue(r.Username),
		sanitizeLogValue(r.Protocol),
		r.Date.Format("[02/Jan/2006:15:04:05 -0700]"),
		`"` + sanitizeLogValue(r.Method) + " " + sanitizeLogValue(r.URI) + `"`,
		strconv.Itoa(r.Status),
		strconv.Itoa(r.Size),
		sanitizeLogValue(r.Referer),
		sanitizeLogValue(r.UserAgent),
	}, " ")
}

// String implements fmt.Stringer interface and produces a log entry using RequestInfo.CLFExtendedString.
//
// Deprecated: use RequestInfo.String from github.com/LerianStudio/lib-observability/middleware.
func (r *RequestInfo) String() string {
	return r.CLFString()
}

// FinishRequestInfo calculates the duration of RequestInfo automatically using time.Now()
// It also set StatusCode and Size of RequestInfo passed by ResponseMetricsWrapper.
//
// Deprecated: use RequestInfo.FinishRequestInfo from github.com/LerianStudio/lib-observability/middleware.
func (r *RequestInfo) FinishRequestInfo(rw *ResponseMetricsWrapper) {
	if rw == nil {
		return
	}

	r.Duration = time.Now().UTC().Sub(r.Date)
	r.Status = rw.StatusCode
	r.Size = rw.Size
}

// handleJSONBody obfuscates sensitive fields in a JSON request body.
// Handles both top-level objects and arrays.
func handleJSONBody(bodyBytes []byte) string {
	var bodyData any
	if err := json.Unmarshal(bodyBytes, &bodyData); err != nil {
		return string(bodyBytes)
	}

	switch v := bodyData.(type) {
	case map[string]any:
		obfuscateMapRecursively(v, 0)
	case []any:
		obfuscateSliceRecursively(v, 0)
	default:
		return string(bodyBytes)
	}

	updatedBody, err := json.Marshal(bodyData)
	if err != nil {
		return string(bodyBytes)
	}

	return string(updatedBody)
}
