package validators

import (
	"errors"
	"strings"
	"slices"
)

// Basic implements the BasicValidator interface using existing lib-commons functions
// This ensures compatibility with existing code while providing a structured approach.
type Basic struct{}

// NewBasic creates a new Basic validator
func NewBasic() *Basic {
	return &Basic{}
}

// ValidateType validates if an asset type is valid
func (b *Basic) ValidateType(assetType string) error {
	// lib-commons currently expects lowercase types
	types := []string{"crypto", "currency", "commodity", "others"}
	
	if !slices.Contains(types, strings.ToLower(assetType)) {
		return errors.New(ErrInvalidAssetType)
	}
	
	return nil
}

// ValidateAccountType validates if an account type is valid
func (b *Basic) ValidateAccountType(accountType string) error {
	types := []string{"deposit", "savings", "loans", "marketplace", "creditCard"}
	
	if !slices.Contains(types, accountType) {
		return errors.New(ErrInvalidAccountType)
	}
	
	return nil
}

// ValidateCurrency validates if a currency code is valid
func (b *Basic) ValidateCurrency(code string) error {
	// Using a subset of currencies to keep this function small
	// A full implementation would include all ISO 4217 codes plus common crypto codes
	currencies := []string{
		// Fiat currencies (ISO 4217)
		"AED", "AFN", "ALL", "AMD", "ANG", "AOA", "ARS", "AUD", "AWG", "AZN", "BAM", "BBD", "BDT", "BGN", "BHD", "BIF", "BMD", "BND", "BOB",
		"BOV", "BRL", "BSD", "BTN", "BWP", "BYN", "BZD", "CAD", "CDF", "CHE", "CHF", "CHW", "CLF", "CLP", "CNY", "COP", "COU", "CRC", "CUC",
		"CUP", "CVE", "CZK", "DJF", "DKK", "DOP", "DZD", "EGP", "ERN", "ETB", "EUR", "FJD", "FKP", "GBP", "GEL", "GHS", "GIP", "GMD", "GNF",
		"GTQ", "GYD", "HKD", "HNL", "HTG", "HUF", "IDR", "ILS", "INR", "IQD", "IRR", "ISK", "JMD", "JOD", "JPY", "KES", "KGS", "KHR", "KMF",
		"KPW", "KRW", "KWD", "KYD", "KZT", "LAK", "LBP", "LKR", "LRD", "LSL", "LYD", "MAD", "MDL", "MGA", "MKD", "MMK", "MNT", "MOP", "MRU",
		"MUR", "MVR", "MWK", "MXN", "MXV", "MYR", "MZN", "NAD", "NGN", "NIO", "NOK", "NPR", "NZD", "OMR", "PAB", "PEN", "PGK", "PHP", "PKR",
		"PLN", "PYG", "QAR", "RON", "RSD", "RUB", "RWF", "SAR", "SBD", "SCR", "SDG", "SEK", "SGD", "SHP", "SLE", "SOS", "SRD", "SSP", "STN",
		"SVC", "SYP", "SZL", "THB", "TJS", "TMT", "TND", "TOP", "TRY", "TTD", "TWD", "TZS", "UAH", "UGX", "USD", "USN", "UYI", "UYU", "UZS",
		"VED", "VEF", "VND", "VUV", "WST", "XAF", "XCD", "XDR", "XOF", "XPF", "XSU", "XUA", "YER", "ZAR", "ZMW", "ZWL",
		
		// Cryptocurrencies
		"BTC", "ETH", "USDT", "USDC", "XRP", "SOL", "ADA", "DOGE", "AVAX", "DOT",
		"MATIC", "TRX", "LINK", "SHIB", "LTC", "BCH", "XLM", "ATOM", "UNI", "XMR",
	}
	
	if !slices.Contains(currencies, code) {
		return errors.New(ErrInvalidCurrency)
	}
	
	return nil
}

// ValidateCountryAddress validates if a country code is valid
func (b *Basic) ValidateCountryAddress(code string) error {
	countries := []string{
		"AD", "AE", "AF", "AG", "AI", "AL", "AM", "AO", "AQ", "AR", "AS", "AT", "AU", "AW", "AX", "AZ",
		"BA", "BB", "BD", "BE", "BF", "BG", "BH", "BI", "BJ", "BL", "BM", "BN", "BO", "BQ", "BR", "BS", "BT", "BV", "BW",
		"BY", "BZ", "CA", "CC", "CD", "CF", "CG", "CH", "CI", "CK", "CL", "CM", "CN", "CO", "CR", "CU", "CV", "CW", "CX",
		"CY", "CZ", "DE", "DJ", "DK", "DM", "DO", "DZ", "EC", "EE", "EG", "EH", "ER", "ES", "ET", "FI", "FJ", "FK", "FM",
		"FO", "FR", "GA", "GB", "GD", "GE", "GF", "GG", "GH", "GI", "GL", "GM", "GN", "GP", "GQ", "GR", "GS", "GT", "GU",
		"GW", "GY", "HK", "HM", "HN", "HR", "HT", "HU", "ID", "IE", "IL", "IM", "IN", "IO", "IQ", "IR", "IS", "IT", "JE",
		"JM", "JO", "JP", "KE", "KG", "KH", "KI", "KM", "KN", "KP", "KR", "KW", "KY", "KZ", "LA", "LB", "LC", "LI", "LK",
		"LR", "LS", "LT", "LU", "LV", "LY", "MA", "MC", "MD", "ME", "MF", "MG", "MH", "MK", "ML", "MM", "MN", "MO", "MP",
		"MQ", "MR", "MS", "MT", "MU", "MV", "MW", "MX", "MY", "MZ", "NA", "NC", "NE", "NF", "NG", "NI", "NL", "NO", "NP",
		"NR", "NU", "NZ", "OM", "PA", "PE", "PF", "PG", "PH", "PK", "PL", "PM", "PN", "PR", "PS", "PT", "PW", "PY", "QA",
		"RE", "RO", "RS", "RU", "RW", "SA", "SB", "SC", "SD", "SE", "SG", "SH", "SI", "SJ", "SK", "SL", "SM", "SN", "SO",
		"SR", "SS", "ST", "SV", "SX", "SY", "SZ", "TC", "TD", "TF", "TG", "TH", "TJ", "TK", "TL", "TM", "TN", "TO", "TR",
		"TT", "TV", "TW", "TZ", "UA", "UG", "UM", "US", "UY", "UZ", "VA", "VC", "VE", "VG", "VI", "VN", "VU", "WF", "WS",
		"YE", "YT", "ZA", "ZM", "ZW",
	}

	if !slices.Contains(countries, code) {
		return errors.New(ErrInvalidCountry)
	}
	
	return nil
}

// Default validator instance for convenience
var Default = NewBasic()

// ValidateAssetCode checks if an asset code is valid according to the AssetCodePattern
func ValidateAssetCode(code string) error {
	if code == "" {
		return errors.New("0032") // Using existing error codes for compatibility
	}

	if !AssetCodePattern.MatchString(code) {
		return errors.New("0033")
	}

	return nil
}

// ValidateAccountAlias checks if an account alias is valid according to the AccountAliasPattern
func ValidateAccountAlias(alias string) error {
	if alias == "" {
		return errors.New("0066")
	}

	if !AccountAliasPattern.MatchString(alias) {
		return errors.New("0067")
	}

	return nil
}

// ValidateTransactionCode checks if a transaction code is valid
func ValidateTransactionCode(code string) error {
	if code == "" {
		return errors.New("0032")
	}

	if !TransactionCodePattern.MatchString(code) {
		return errors.New("0033")
	}

	return nil
}