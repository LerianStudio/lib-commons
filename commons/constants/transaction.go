package constant

const (
	// DefaultExternalAccountAliasPrefix prefixes aliases for external accounts.
	DefaultExternalAccountAliasPrefix = "@external/"
	// ExternalAccountType identifies external accounts.
	ExternalAccountType = "external"

	// DEBIT identifies debit operations.
	DEBIT = "DEBIT"
	// CREDIT identifies credit operations.
	CREDIT = "CREDIT"
	// ONHOLD identifies hold operations.
	ONHOLD = "ON_HOLD"
	// RELEASE identifies release operations.
	RELEASE = "RELEASE"

	// CREATED identifies transaction intents created but not yet approved.
	CREATED = "CREATED"
	// APPROVED identifies transaction intents approved for processing.
	APPROVED = "APPROVED"
	// PENDING identifies transaction intents currently being processed.
	PENDING = "PENDING"
	// CANCELED identifies transaction intents canceled or rolled back.
	CANCELED = "CANCELED"
	// NOTED identifies transaction intents that have been noted/acknowledged.
	NOTED = "NOTED"
)
