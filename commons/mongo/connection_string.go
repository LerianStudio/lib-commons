// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package mongo

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
)

// BuildConnectionString constructs a properly formatted MongoDB connection string.
//
// Features:
//   - URL-encodes credentials (handles special characters like @, :, /)
//   - Omits port for mongodb+srv URIs (SRV discovery doesn't use ports)
//   - Handles empty credentials gracefully (connects without auth)
//   - Optionally logs masked connection string for debugging
//
// Parameters:
//   - scheme: "mongodb" or "mongodb+srv"
//   - user: username (will be URL-encoded)
//   - password: password (will be URL-encoded)
//   - host: MongoDB host address
//   - port: port number (ignored for mongodb+srv)
//   - parameters: query parameters (e.g., "replicaSet=rs0&authSource=admin")
//   - logger: optional logger for debug output (credentials masked)
//
// Returns the complete connection string ready for use with MongoDB drivers.
func BuildConnectionString(scheme, user, password, host, port, parameters string, logger log.Logger) string {
	var connectionString string

	credentialsPart := buildCredentialsPart(user, password)
	hostPart := buildHostPart(scheme, host, port)

	if credentialsPart != "" {
		connectionString = fmt.Sprintf("%s://%s@%s/", scheme, credentialsPart, hostPart)
	} else {
		connectionString = fmt.Sprintf("%s://%s/", scheme, hostPart)
	}

	if parameters != "" {
		connectionString += "?" + parameters
	}

	if logger != nil {
		logMaskedConnectionString(logger, scheme, hostPart, parameters, credentialsPart != "")
	}

	return connectionString
}

func buildCredentialsPart(user, password string) string {
	if user == "" {
		return ""
	}

	return url.UserPassword(user, password).String()
}

func buildHostPart(scheme, host, port string) string {
	if strings.HasPrefix(scheme, "mongodb+srv") {
		return host
	}

	if port != "" {
		return fmt.Sprintf("%s:%s", host, port)
	}

	return host
}

func logMaskedConnectionString(logger log.Logger, scheme, hostPart, parameters string, hasCredentials bool) {
	var maskedConnStr string

	if hasCredentials {
		maskedConnStr = fmt.Sprintf("%s://<credentials>@%s/", scheme, hostPart)
	} else {
		maskedConnStr = fmt.Sprintf("%s://%s/", scheme, hostPart)
	}

	if parameters != "" {
		maskedConnStr += "?" + parameters
	}

	logger.Debugf("MongoDB connection string built: %s", maskedConnStr)
}
