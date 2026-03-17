package http

import (
	"net/url"
	"strings"

	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/security"
	"github.com/gofiber/fiber/v2"
)

// getBodyObfuscatedString returns the request body with sensitive fields obfuscated.
func getBodyObfuscatedString(c *fiber.Ctx, bodyBytes []byte) string {
	contentType := c.Get(cn.HeaderContentType)

	var obfuscatedBody string

	switch {
	case strings.Contains(contentType, "application/json"):
		obfuscatedBody = handleJSONBody(bodyBytes)
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		obfuscatedBody = handleURLEncodedBody(bodyBytes)
	case strings.Contains(contentType, "multipart/form-data"):
		obfuscatedBody = handleMultipartBody(c)
	default:
		obfuscatedBody = string(bodyBytes)
	}

	return obfuscatedBody
}

// obfuscateMapRecursively replaces sensitive map values up to maxObfuscationDepth levels.
func obfuscateMapRecursively(data map[string]any, depth int) {
	if depth >= maxObfuscationDepth {
		return
	}

	for key, value := range data {
		if security.IsSensitiveField(key) {
			data[key] = cn.ObfuscatedValue
			continue
		}

		switch v := value.(type) {
		case map[string]any:
			obfuscateMapRecursively(v, depth+1)
		case []any:
			obfuscateSliceRecursively(v, depth+1)
		}
	}
}

// obfuscateSliceRecursively walks slice elements and obfuscates nested sensitive fields.
func obfuscateSliceRecursively(data []any, depth int) {
	if depth >= maxObfuscationDepth {
		return
	}

	for _, item := range data {
		switch v := item.(type) {
		case map[string]any:
			obfuscateMapRecursively(v, depth+1)
		case []any:
			obfuscateSliceRecursively(v, depth+1)
		}
	}
}

// handleURLEncodedBody obfuscates sensitive fields in a URL-encoded request body.
func handleURLEncodedBody(bodyBytes []byte) string {
	formData, err := url.ParseQuery(string(bodyBytes))
	if err != nil {
		return string(bodyBytes)
	}

	updatedBody := url.Values{}

	for key, values := range formData {
		if security.IsSensitiveField(key) {
			for range values {
				updatedBody.Add(key, cn.ObfuscatedValue)
			}
		} else {
			for _, value := range values {
				updatedBody.Add(key, value)
			}
		}
	}

	return updatedBody.Encode()
}

// handleMultipartBody obfuscates sensitive fields in a multipart/form-data request body.
func handleMultipartBody(c *fiber.Ctx) string {
	form, err := c.MultipartForm()
	if err != nil {
		return "[multipart/form-data]"
	}

	result := url.Values{}

	for key, values := range form.Value {
		if security.IsSensitiveField(key) {
			for range values {
				result.Add(key, cn.ObfuscatedValue)
			}
		} else {
			for _, value := range values {
				result.Add(key, value)
			}
		}
	}

	for key := range form.File {
		if security.IsSensitiveField(key) {
			result.Add(key, cn.ObfuscatedValue)
		} else {
			result.Add(key, "[file]")
		}
	}

	return result.Encode()
}
