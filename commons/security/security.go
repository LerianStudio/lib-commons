// Package security provides comprehensive security utilities including input validation,
// cryptographic operations, secure configuration management, and vulnerability assessment.
package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// SecurityLevel represents different security classification levels
type SecurityLevel string

const (
	SecurityLevelPublic       SecurityLevel = "public"
	SecurityLevelInternal     SecurityLevel = "internal"
	SecurityLevelConfidential SecurityLevel = "confidential"
	SecurityLevelRestricted   SecurityLevel = "restricted"
	SecurityLevelTopSecret    SecurityLevel = "top_secret"
)

// SecurityConfig defines comprehensive security configuration
type SecurityConfig struct {
	// Cryptographic settings
	PasswordMinLength     int           `json:"password_min_length"`
	PasswordRequireUpper  bool          `json:"password_require_upper"`
	PasswordRequireLower  bool          `json:"password_require_lower"`
	PasswordRequireNumber bool          `json:"password_require_number"`
	PasswordRequireSymbol bool          `json:"password_require_symbol"`
	BcryptCost            int           `json:"bcrypt_cost"`
	TokenExpiration       time.Duration `json:"token_expiration"`

	// Input validation settings
	MaxInputLength      int      `json:"max_input_length"`
	AllowedContentTypes []string `json:"allowed_content_types"`
	BlockedDomains      []string `json:"blocked_domains"`
	AllowedIPRanges     []string `json:"allowed_ip_ranges"`

	// Rate limiting and throttling
	MaxRequestsPerMinute int           `json:"max_requests_per_minute"`
	BruteForceThreshold  int           `json:"brute_force_threshold"`
	BruteForceWindow     time.Duration `json:"brute_force_window"`
	BruteForceBlockTime  time.Duration `json:"brute_force_block_time"`

	// Security headers and policies
	EnableCSP             bool     `json:"enable_csp"`
	CSPDirectives         []string `json:"csp_directives"`
	EnableHSTS            bool     `json:"enable_hsts"`
	HSTSMaxAge            int      `json:"hsts_max_age"`
	EnableFrameDeny       bool     `json:"enable_frame_deny"`
	EnableContentTypeDeny bool     `json:"enable_content_type_deny"`

	// Audit and monitoring
	EnableAuditLogging bool     `json:"enable_audit_logging"`
	AuditLogLevel      string   `json:"audit_log_level"`
	SecurityEventTypes []string `json:"security_event_types"`

	// Compliance settings
	ComplianceStandards   []string      `json:"compliance_standards"` // GDPR, HIPAA, PCI-DSS, SOX
	DataRetentionPeriod   time.Duration `json:"data_retention_period"`
	RequireDataEncryption bool          `json:"require_data_encryption"`
}

// DefaultSecurityConfig returns a production-ready security configuration
func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		// Strong password requirements
		PasswordMinLength:     12,
		PasswordRequireUpper:  true,
		PasswordRequireLower:  true,
		PasswordRequireNumber: true,
		PasswordRequireSymbol: true,
		BcryptCost:            12, // High cost for production
		TokenExpiration:       15 * time.Minute,

		// Strict input validation
		MaxInputLength: 10000,
		AllowedContentTypes: []string{
			"application/json",
			"application/x-www-form-urlencoded",
			"multipart/form-data",
		},
		BlockedDomains:  []string{"tempmail.org", "10minutemail.com", "guerrillamail.com"},
		AllowedIPRanges: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},

		// Rate limiting protection
		MaxRequestsPerMinute: 100,
		BruteForceThreshold:  5,
		BruteForceWindow:     5 * time.Minute,
		BruteForceBlockTime:  30 * time.Minute,

		// Security headers enabled
		EnableCSP: true,
		CSPDirectives: []string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline'",
			"style-src 'self' 'unsafe-inline'",
		},
		EnableHSTS:            true,
		HSTSMaxAge:            31536000, // 1 year
		EnableFrameDeny:       true,
		EnableContentTypeDeny: true,

		// Comprehensive audit logging
		EnableAuditLogging: true,
		AuditLogLevel:      "info",
		SecurityEventTypes: []string{
			"login",
			"logout",
			"failed_auth",
			"privilege_escalation",
			"data_access",
			"configuration_change",
		},

		// Compliance defaults
		ComplianceStandards:   []string{"GDPR", "SOC2"},
		DataRetentionPeriod:   7 * 24 * time.Hour, // 7 days for security logs
		RequireDataEncryption: true,
	}
}

// SecurityValidator provides comprehensive security validation
type SecurityValidator struct {
	config SecurityConfig
}

// NewSecurityValidator creates a new security validator with the given configuration
func NewSecurityValidator(config SecurityConfig) *SecurityValidator {
	return &SecurityValidator{
		config: config,
	}
}

// ValidatePassword validates password strength according to security policy
func (sv *SecurityValidator) ValidatePassword(password string) error {
	if len(password) < sv.config.PasswordMinLength {
		return fmt.Errorf(
			"password must be at least %d characters long",
			sv.config.PasswordMinLength,
		)
	}

	hasUpper := false
	hasLower := false
	hasNumber := false
	hasSymbol := false

	for _, char := range password {
		switch {
		case char >= 'A' && char <= 'Z':
			hasUpper = true
		case char >= 'a' && char <= 'z':
			hasLower = true
		case char >= '0' && char <= '9':
			hasNumber = true
		case strings.ContainsRune("!@#$%^&*()_+-=[]{}|;:,.<>?", char):
			hasSymbol = true
		}
	}

	if sv.config.PasswordRequireUpper && !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if sv.config.PasswordRequireLower && !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if sv.config.PasswordRequireNumber && !hasNumber {
		return fmt.Errorf("password must contain at least one number")
	}
	if sv.config.PasswordRequireSymbol && !hasSymbol {
		return fmt.Errorf("password must contain at least one symbol")
	}

	// Check for common patterns
	if isCommonPassword(password) {
		return fmt.Errorf("password is too common, please choose a more unique password")
	}

	return nil
}

// ValidateInput validates user input against injection attacks and malicious content
func (sv *SecurityValidator) ValidateInput(input string, inputType string) error {
	if len(input) > sv.config.MaxInputLength {
		return fmt.Errorf("input exceeds maximum length of %d characters", sv.config.MaxInputLength)
	}

	// Check for SQL injection patterns
	if containsSQLInjection(input) {
		return fmt.Errorf("input contains potential SQL injection patterns")
	}

	// Check for XSS patterns
	if containsXSS(input) {
		return fmt.Errorf("input contains potential XSS patterns")
	}

	// Check for command injection
	if containsCommandInjection(input) {
		return fmt.Errorf("input contains potential command injection patterns")
	}

	// Check for LDAP injection
	if containsLDAPInjection(input) {
		return fmt.Errorf("input contains potential LDAP injection patterns")
	}

	// Type-specific validation
	switch inputType {
	case "email":
		return sv.ValidateEmail(input)
	case "url":
		return sv.ValidateURL(input)
	case "ip":
		return sv.ValidateIPAddress(input)
	case "domain":
		return sv.ValidateDomain(input)
	}

	return nil
}

// ValidateEmail validates email addresses with security considerations
func (sv *SecurityValidator) ValidateEmail(email string) error {
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(email) {
		return fmt.Errorf("invalid email format")
	}

	// Check against blocked domains
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid email format")
	}

	domain := strings.ToLower(parts[1])
	for _, blockedDomain := range sv.config.BlockedDomains {
		if domain == strings.ToLower(blockedDomain) {
			return fmt.Errorf("email domain is not allowed")
		}
	}

	return nil
}

// ValidateURL validates URLs and checks for malicious content
func (sv *SecurityValidator) ValidateURL(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}

	// Only allow HTTP and HTTPS
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only HTTP and HTTPS URLs are allowed")
	}

	// Check for suspicious patterns
	if strings.Contains(u.Host, "localhost") || strings.Contains(u.Host, "127.0.0.1") {
		return fmt.Errorf("localhost URLs are not allowed")
	}

	// Check for internal IP ranges
	if ip := net.ParseIP(u.Host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() {
			return fmt.Errorf("private IP addresses are not allowed")
		}
	}

	return nil
}

// ValidateIPAddress validates IP addresses and checks against allowed ranges
func (sv *SecurityValidator) ValidateIPAddress(ipStr string) error {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("invalid IP address format")
	}

	// Check against allowed IP ranges if configured
	if len(sv.config.AllowedIPRanges) > 0 {
		allowed := false
		for _, rangeStr := range sv.config.AllowedIPRanges {
			_, ipNet, err := net.ParseCIDR(rangeStr)
			if err != nil {
				continue
			}
			if ipNet.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("IP address is not in allowed ranges")
		}
	}

	return nil
}

// ValidateDomain validates domain names
func (sv *SecurityValidator) ValidateDomain(domain string) error {
	// Basic domain validation
	domainRegex := regexp.MustCompile(
		`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`,
	)
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain format")
	}

	// Check against blocked domains
	for _, blockedDomain := range sv.config.BlockedDomains {
		if strings.EqualFold(domain, blockedDomain) {
			return fmt.Errorf("domain is blocked")
		}
	}

	return nil
}

// CryptographicManager handles secure cryptographic operations
type CryptographicManager struct {
	config SecurityConfig
}

// NewCryptographicManager creates a new cryptographic manager
func NewCryptographicManager(config SecurityConfig) *CryptographicManager {
	return &CryptographicManager{
		config: config,
	}
}

// HashPassword creates a secure hash of the password using bcrypt
func (cm *CryptographicManager) HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cm.config.BcryptCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword verifies a password against its hash using constant-time comparison
func (cm *CryptographicManager) VerifyPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// HashPasswordArgon2 creates a secure hash using Argon2 (more secure than bcrypt)
func (cm *CryptographicManager) HashPasswordArgon2(password, salt string) string {
	if salt == "" {
		salt = cm.GenerateSecureToken(32)
	}

	hash := argon2.IDKey([]byte(password), []byte(salt), 1, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=1,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString([]byte(salt)),
		base64.RawStdEncoding.EncodeToString(hash))
}

// VerifyPasswordArgon2 verifies an Argon2 password hash
func (cm *CryptographicManager) VerifyPasswordArgon2(password, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	actualHash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)

	return subtle.ConstantTimeCompare(expectedHash, actualHash) == 1
}

// GenerateSecureToken generates a cryptographically secure random token
func (cm *CryptographicManager) GenerateSecureToken(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("failed to generate secure token: %v", err))
	}
	return hex.EncodeToString(bytes)
}

// GenerateAPIKey generates a secure API key with specific format
func (cm *CryptographicManager) GenerateAPIKey(prefix string) string {
	tokenPart := cm.GenerateSecureToken(32)
	if prefix == "" {
		prefix = "lsc" // LerianStudio Commons
	}
	return fmt.Sprintf("%s_%s", prefix, tokenPart)
}

// SecurityAuditor performs security assessments and vulnerability checks
type SecurityAuditor struct {
	config SecurityConfig
}

// NewSecurityAuditor creates a new security auditor
func NewSecurityAuditor(config SecurityConfig) *SecurityAuditor {
	return &SecurityAuditor{
		config: config,
	}
}

// VulnerabilityReport represents a security vulnerability assessment
type VulnerabilityReport struct {
	Timestamp   time.Time              `json:"timestamp"`
	Severity    string                 `json:"severity"` // low, medium, high, critical
	Category    string                 `json:"category"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Affected    []string               `json:"affected"`
	Remediation string                 `json:"remediation"`
	References  []string               `json:"references"`
	CVSS        float64                `json:"cvss_score"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// AuditSecurityConfiguration performs a comprehensive security configuration audit
func (sa *SecurityAuditor) AuditSecurityConfiguration(ctx context.Context) []VulnerabilityReport {
	var reports []VulnerabilityReport

	// Check password policy strength
	if sa.config.PasswordMinLength < 12 {
		reports = append(reports, VulnerabilityReport{
			Timestamp:   time.Now(),
			Severity:    "medium",
			Category:    "authentication",
			Title:       "Weak Password Policy",
			Description: "Password minimum length is below recommended 12 characters",
			Remediation: "Increase minimum password length to at least 12 characters",
			CVSS:        5.3,
		})
	}

	// Check bcrypt cost
	if sa.config.BcryptCost < 12 {
		reports = append(reports, VulnerabilityReport{
			Timestamp:   time.Now(),
			Severity:    "medium",
			Category:    "cryptography",
			Title:       "Weak Password Hashing",
			Description: "Bcrypt cost factor is below recommended minimum of 12",
			Remediation: "Increase bcrypt cost to at least 12 for production use",
			CVSS:        5.8,
		})
	}

	// Check token expiration
	if sa.config.TokenExpiration > 24*time.Hour {
		reports = append(reports, VulnerabilityReport{
			Timestamp:   time.Now(),
			Severity:    "low",
			Category:    "session_management",
			Title:       "Long Token Expiration",
			Description: "Token expiration time exceeds 24 hours",
			Remediation: "Reduce token expiration to maximum 24 hours",
			CVSS:        3.1,
		})
	}

	// Check security headers
	if !sa.config.EnableHSTS {
		reports = append(reports, VulnerabilityReport{
			Timestamp:   time.Now(),
			Severity:    "medium",
			Category:    "transport_security",
			Title:       "Missing HSTS Header",
			Description: "HTTP Strict Transport Security (HSTS) is not enabled",
			Remediation: "Enable HSTS headers to prevent protocol downgrade attacks",
			CVSS:        4.3,
		})
	}

	if !sa.config.EnableCSP {
		reports = append(reports, VulnerabilityReport{
			Timestamp:   time.Now(),
			Severity:    "medium",
			Category:    "web_security",
			Title:       "Missing Content Security Policy",
			Description: "Content Security Policy (CSP) headers are not configured",
			Remediation: "Implement CSP headers to prevent XSS attacks",
			CVSS:        6.1,
		})
	}

	// Check audit logging
	if !sa.config.EnableAuditLogging {
		reports = append(reports, VulnerabilityReport{
			Timestamp:   time.Now(),
			Severity:    "high",
			Category:    "monitoring",
			Title:       "Missing Audit Logging",
			Description: "Security audit logging is not enabled",
			Remediation: "Enable comprehensive audit logging for security events",
			CVSS:        7.5,
		})
	}

	return reports
}

// Helper functions for injection detection

func containsSQLInjection(input string) bool {
	sqlPatterns := []string{
		"'", "\"", ";", "--", "/*", "*/", "xp_", "sp_",
		"union", "select", "insert", "update", "delete", "drop",
		"exec", "execute", "declare", "create", "alter",
	}

	lower := strings.ToLower(input)
	for _, pattern := range sqlPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func containsXSS(input string) bool {
	xssPatterns := []string{
		"<script", "</script>", "javascript:", "vbscript:", "onload=",
		"onerror=", "onclick=", "onmouseover=", "onfocus=", "onblur=",
		"<iframe", "<object", "<embed", "<applet", "expression(",
	}

	lower := strings.ToLower(input)
	for _, pattern := range xssPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func containsCommandInjection(input string) bool {
	cmdPatterns := []string{
		"|", "&", ";", "$", "`", "$(", "${", "&&", "||",
		"/bin/", "/usr/bin/", "cmd.exe", "powershell",
		"system(", "exec(", "shell_exec(", "passthru(",
	}

	lower := strings.ToLower(input)
	for _, pattern := range cmdPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func containsLDAPInjection(input string) bool {
	ldapPatterns := []string{
		"*", "(", ")", "\\", "/", "+", "<", ">", "\"", "'",
		"&", "|", "!", "=", "~", "objectclass=", "cn=", "uid=",
	}

	for _, pattern := range ldapPatterns {
		if strings.Contains(input, pattern) {
			return true
		}
	}
	return false
}

func isCommonPassword(password string) bool {
	commonPasswords := []string{
		"password", "123456", "password123", "admin", "qwerty",
		"letmein", "welcome", "monkey", "dragon", "princess",
		"123456789", "password1", "abc123", "iloveyou", "adobe123",
	}

	lower := strings.ToLower(password)
	for _, common := range commonPasswords {
		if lower == common {
			return true
		}
	}
	return false
}
