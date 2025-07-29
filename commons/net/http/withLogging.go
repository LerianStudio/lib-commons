package http

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/commons"
	cn "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/LerianStudio/lib-commons/commons/security"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// RequestInfo is a struct design to store http access log data.
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

// ResponseMetricsWrapper is a Wrapper responsible for collect the response data such as status code and size
// It implements built-in ResponseWriter interface.
type ResponseMetricsWrapper struct {
	Context    *fiber.Ctx
	StatusCode int
	Size       int
	Body       string
}

// NewRequestInfo creates an instance of RequestInfo.
func NewRequestInfo(c *fiber.Ctx) *RequestInfo {
	username, referer := "-", "-"
	rawURL := string(c.Request().URI().FullURI())

	parsedURL, err := url.Parse(rawURL)
	if err == nil && parsedURL.User != nil {
		if name := parsedURL.User.Username(); name != "" {
			username = name
		}
	}

	if c.Get("Referer") != "" {
		referer = c.Get("Referer")
	}

	body := ""

	if c.Request().Header.ContentLength() > 0 {
		bodyBytes := c.Body()

		// Use shared sensitive fields list for consistent obfuscation across the library
		body = getBodyObfuscatedString(c, bodyBytes, security.DefaultSensitiveFields())
	}

	return &RequestInfo{
		TraceID:       c.Get(cn.HeaderID),
		Method:        c.Method(),
		URI:           c.OriginalURL(),
		Username:      username,
		Referer:       referer,
		UserAgent:     c.Get(cn.HeaderUserAgent),
		RemoteAddress: c.IP(),
		Protocol:      c.Protocol(),
		Date:          time.Now().UTC(),
		Body:          body,
	}
}

// CLFString produces a log entry format similar to Common Log Format (CLF)
// Ref: https://httpd.apache.org/docs/trunk/logs.html#common
func (r *RequestInfo) CLFString() string {
	return strings.Join([]string{
		r.RemoteAddress,
		"-",
		r.Username,
		r.Protocol,
		`"` + r.Method + " " + r.URI + `"`,
		strconv.Itoa(r.Status),
		strconv.Itoa(r.Size),
		r.Referer,
		r.UserAgent,
	}, " ")
}

// String implements fmt.Stringer interface and produces a log entry using RequestInfo.CLFExtendedString.
func (r *RequestInfo) String() string {
	return r.CLFString()
}

func (r *RequestInfo) debugRequestString() string {
	return strings.Join([]string{
		r.CLFString(),
		r.Referer,
		r.UserAgent,
		r.Body,
	}, " ")
}

// FinishRequestInfo calculates the duration of RequestInfo automatically using time.Now()
// It also set StatusCode and Size of RequestInfo passed by ResponseMetricsWrapper.
func (r *RequestInfo) FinishRequestInfo(rw *ResponseMetricsWrapper) {
	r.Duration = time.Now().UTC().Sub(r.Date)
	r.Status = rw.StatusCode
	r.Size = rw.Size
}

type logMiddleware struct {
	Logger log.Logger
}

// LogMiddlewareOption represents the log middleware function as an implementation.
type LogMiddlewareOption func(l *logMiddleware)

// WithCustomLogger is a functional option for logMiddleware.
func WithCustomLogger(logger log.Logger) LogMiddlewareOption {
	return func(l *logMiddleware) {
		l.Logger = logger
	}
}

// buildOpts creates an instance of logMiddleware with options.
func buildOpts(opts ...LogMiddlewareOption) *logMiddleware {
	mid := &logMiddleware{
		Logger: &log.GoLogger{},
	}

	for _, opt := range opts {
		opt(mid)
	}

	return mid
}

// WithHTTPLogging is a middleware to log access to http server.
// It logs access log according to Apache Standard Logs which uses Common Log Format (CLF)
// Ref: https://httpd.apache.org/docs/trunk/logs.html#common
func WithHTTPLogging(opts ...LogMiddlewareOption) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Path() == "/health" {
			return c.Next()
		}

		if strings.Contains(c.Path(), "swagger") && c.Path() != "/swagger/index.html" {
			return c.Next()
		}

		setRequestHeaderID(c)

		info := NewRequestInfo(c)

		headerID := c.Get(cn.HeaderID)

		mid := buildOpts(opts...)
		logger := mid.Logger.WithFields(
			cn.HeaderID, info.TraceID,
		).WithDefaultMessageTemplate(headerID + cn.LoggerDefaultSeparator)

		rw := ResponseMetricsWrapper{
			Context:    c,
			StatusCode: 200,
			Size:       0,
			Body:       "",
		}

		logger.Info(info.debugRequestString())

		ctx := commons.ContextWithLogger(c.UserContext(), logger)

		c.SetUserContext(ctx)

		info.FinishRequestInfo(&rw)

		if err := c.Next(); err != nil {
			return err
		}

		return nil
	}
}

// WithGrpcLogging is a gRPC unary interceptor to log access to gRPC server.
func WithGrpcLogging(opts ...LogMiddlewareOption) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			headerID := md.Get(cn.MetadataID)
			if headerID != nil && !commons.IsNilOrEmpty(&headerID[0]) {
				ctx = commons.ContextWithHeaderID(ctx, headerID[0])
			} else {
				ctx = commons.ContextWithHeaderID(ctx, uuid.New().String())
			}
		}

		mid := buildOpts(opts...)
		logger := mid.Logger.WithDefaultMessageTemplate(commons.NewHeaderIDFromContext(ctx) + cn.LoggerDefaultSeparator)

		ctx = commons.ContextWithLogger(ctx, logger)

		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		logger.Infof("gRPC method: %s, Duration: %s, Error: %v", info.FullMethod, duration, err)

		return resp, err
	}
}

func setRequestHeaderID(c *fiber.Ctx) {
	headerID := c.Get(cn.HeaderID)

	if commons.IsNilOrEmpty(&headerID) {
		headerID = uuid.New().String()
		c.Set(cn.HeaderID, headerID)
		c.Request().Header.Set(cn.HeaderID, headerID)
		c.Response().Header.Set(cn.HeaderID, headerID)
	}

	ctx := commons.ContextWithHeaderID(c.UserContext(), headerID)
	c.SetUserContext(ctx)
}

func getBodyObfuscatedString(c *fiber.Ctx, bodyBytes []byte, fieldsToObfuscate []string) string {
	contentType := c.Get("Content-Type")

	var obfuscatedBody string

	if strings.Contains(contentType, "application/json") {
		obfuscatedBody = handleJSONBody(bodyBytes, fieldsToObfuscate)
	} else if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		obfuscatedBody = handleURLFormBody(c, fieldsToObfuscate)
	} else if strings.Contains(contentType, "multipart/form-data") {
		obfuscatedBody = handleMultipartFormBody(c, fieldsToObfuscate)
	} else {
		obfuscatedBody = string(bodyBytes)
	}

	return obfuscatedBody
}

func handleJSONBody(bodyBytes []byte, fieldsToObfuscate []string) string {
	var bodyData map[string]any
	if err := json.Unmarshal(bodyBytes, &bodyData); err != nil {
		return string(bodyBytes)
	}

	for _, field := range fieldsToObfuscate {
		if _, exists := bodyData[field]; exists {
			bodyData[field] = cn.ObfuscatedValue
		}
	}

	updatedBody, err := json.Marshal(bodyData)
	if err != nil {
		return string(bodyBytes)
	}

	return string(updatedBody)
}

func handleURLFormBody(c *fiber.Ctx, fieldsToObfuscate []string) string {
	formData := c.AllParams()

	for _, field := range fieldsToObfuscate {
		if value := c.FormValue(field); value != "" {
			formData[field] = cn.ObfuscatedValue
		}
	}

	updatedBody := url.Values{}

	for key, value := range formData {
		updatedBody.Set(key, value)
	}

	return updatedBody.Encode()
}

func handleMultipartFormBody(c *fiber.Ctx, fieldsToObfuscate []string) string {
	formData := c.AllParams()
	updatedBody := url.Values{}

	for _, field := range fieldsToObfuscate {
		if _, exists := formData[field]; exists {
			formData[field] = cn.ObfuscatedValue
		}
	}

	for key, value := range formData {
		updatedBody.Set(key, value)
	}

	return updatedBody.Encode()
}
