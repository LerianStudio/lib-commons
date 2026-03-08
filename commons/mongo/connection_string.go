package mongo

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

var (
	// ErrInvalidScheme is returned when URI scheme is not mongodb or mongodb+srv.
	ErrInvalidScheme = errors.New("invalid mongo uri scheme")
	// ErrEmptyHost is returned when URI host is empty.
	ErrEmptyHost = errors.New("mongo uri host cannot be empty")
	// ErrInvalidPort is returned when URI port is outside the valid TCP range.
	ErrInvalidPort = errors.New("mongo uri port is invalid")
	// ErrPortNotAllowedForSRV is returned when a port is provided for mongodb+srv.
	ErrPortNotAllowedForSRV = errors.New("port cannot be set for mongodb+srv")
	// ErrPasswordWithoutUser is returned when password is set without username.
	ErrPasswordWithoutUser = errors.New("password requires username")
)

// URIConfig contains the components used to build a MongoDB URI.
type URIConfig struct {
	Scheme   string
	Username string
	Password string // #nosec G117 -- builder struct for one-time URI construction; password encoded via url.UserPassword()
	Host     string
	Port     string
	Database string
	Query    url.Values
}

// BuildURI validates the structural fields of URIConfig (scheme, host, port,
// credential presence) and assembles a MongoDB connection URI. It does NOT
// perform DNS resolution, full RFC 3986 host validation, or MongoDB
// connstring-level validation — those checks are deferred to the driver's
// connstring.Parse when the URI is actually used to connect.
func BuildURI(cfg URIConfig) (string, error) {
	scheme := strings.TrimSpace(cfg.Scheme)
	host := strings.TrimSpace(cfg.Host)
	port := strings.TrimSpace(cfg.Port)
	database := strings.TrimSpace(cfg.Database)
	username := strings.TrimSpace(cfg.Username)

	if err := validateBuildURIInput(scheme, host, port, username, cfg.Password); err != nil {
		return "", err
	}

	uri := buildURL(scheme, host, port, username, cfg.Password, database, cfg.Query)

	return uri.String(), nil
}

func validateBuildURIInput(scheme, host, port, username, password string) error {
	if err := validateScheme(scheme); err != nil {
		return err
	}

	if host == "" {
		return ErrEmptyHost
	}

	if username == "" && password != "" {
		return ErrPasswordWithoutUser
	}

	if scheme == "mongodb+srv" && port != "" {
		return ErrPortNotAllowedForSRV
	}

	if scheme == "mongodb" {
		if err := validateMongoPort(port); err != nil {
			return err
		}
	}

	return nil
}

func validateScheme(scheme string) error {
	if scheme != "mongodb" && scheme != "mongodb+srv" {
		return ErrInvalidScheme
	}

	return nil
}

func validateMongoPort(port string) error {
	if port == "" {
		return nil
	}

	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return ErrInvalidPort
	}

	return nil
}

func buildURL(scheme, host, port, username, password, database string, query url.Values) *url.URL {
	uri := &url.URL{Scheme: scheme}
	uri.Host = buildHost(host, port)
	uri.User = buildUser(username, password)
	uri.Path = buildPath(database)

	if len(query) > 0 {
		uri.RawQuery = query.Encode()
	}

	return uri
}

// buildHost concatenates host and port. IPv6 addresses are bracketed per
// RFC 3986 to avoid ambiguity with the port separator. The caller is
// responsible for validating that host contains only legitimate hostname
// characters. The mongo driver validates the full URI downstream via
// connstring.Parse.
func buildHost(host, port string) string {
	// Detect raw IPv6 literal (contains ":" but is not already bracketed).
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}

	if port == "" {
		return host
	}

	return host + ":" + port
}

func buildUser(username, password string) *url.Userinfo {
	if username == "" {
		return nil
	}

	// When password is empty, use url.User to produce "username@" instead of
	// "username:@". The trailing colon is technically valid per RFC 3986 but
	// can confuse some drivers and implies an empty password was intentionally set.
	if password == "" {
		return url.User(username)
	}

	return url.UserPassword(username, password)
}

func buildPath(database string) string {
	if database == "" {
		return "/"
	}

	return "/" + url.PathEscape(database)
}
