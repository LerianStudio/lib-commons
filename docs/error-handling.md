# Error Handling

The error handling utilities provide a standardized way to handle business errors across your application with consistent error codes and messages.

## Overview

The error handling system provides:
- Standardized error response structure
- Business error mapping
- Consistent error codes
- Internationalization support

## Error Response Structure

```go
type Response struct {
    Code    string         `json:"code"`
    Message string         `json:"message"`
    Details map[string]any `json:"details,omitempty"`
}
```

## Basic Usage

### Creating Business Errors

```go
import (
    "github.com/LerianStudio/lib-commons/commons"
    constant "github.com/LerianStudio/lib-commons/commons/constants"
)

// Validate business rules
func ValidateAccount(account *Account) error {
    if account.Balance < 0 {
        return commons.ValidateBusinessError(
            constant.ErrInsufficientFunds,
            "Account",
            account.ID,
        )
    }
    return nil
}
```

### Error Constants

Common error constants are defined in `commons/constants/errors.go`:

```go
var (
    // Account errors
    ErrAccountNotFound        = errors.New("0001")
    ErrInsufficientFunds      = errors.New("0002")
    ErrAccountIneligibility   = errors.New("0003")
    ErrAccountStatusConflict  = errors.New("0004")
    
    // Transaction errors
    ErrInvalidTransaction     = errors.New("1001")
    ErrTransactionNotFound    = errors.New("1002")
    ErrDuplicateTransaction   = errors.New("1003")
    
    // Asset errors
    ErrAssetNotFound          = errors.New("2001")
    ErrInvalidAssetCode       = errors.New("2002")
    
    // General errors
    ErrInvalidInput           = errors.New("9001")
    ErrUnauthorized           = errors.New("9002")
    ErrForbidden              = errors.New("9003")
    ErrInternalError          = errors.New("9999")
)
```

## Advanced Usage

### Custom Error Messages

```go
// Using custom error messages with parameters
err := commons.ValidateBusinessError(
    constant.ErrInsufficientFunds,
    "Account",
    accountID,
    requiredAmount,
    currentBalance,
)

// The error will be formatted with the provided parameters
// Example: "Account 123 has insufficient funds. Required: 100, Available: 50"
```

### Error Response with Details

```go
response := commons.Response{
    Code:    "0002",
    Message: "Insufficient funds",
    Details: map[string]any{
        "account_id":      "123",
        "required_amount": 100.50,
        "current_balance": 50.25,
        "currency":        "USD",
    },
}
```

### HTTP Error Handling

```go
func HandleTransaction(w http.ResponseWriter, r *http.Request) {
    err := processTransaction(r.Context())
    if err != nil {
        // Convert to business error
        businessErr := commons.ValidateBusinessError(err, "Transaction")
        
        // Determine HTTP status code
        statusCode := getHTTPStatusCode(businessErr.Code)
        
        // Send error response
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(statusCode)
        json.NewEncoder(w).Encode(businessErr)
        return
    }
    
    // Success response
    w.WriteHeader(http.StatusOK)
}

func getHTTPStatusCode(errorCode string) int {
    switch errorCode {
    case "0001", "1002", "2001": // Not found errors
        return http.StatusNotFound
    case "0002", "0003", "0004": // Business rule violations
        return http.StatusUnprocessableEntity
    case "9001": // Invalid input
        return http.StatusBadRequest
    case "9002": // Unauthorized
        return http.StatusUnauthorized
    case "9003": // Forbidden
        return http.StatusForbidden
    default:
        return http.StatusInternalServerError
    }
}
```

### Error Wrapping and Context

```go
func ProcessPayment(ctx context.Context, payment *Payment) error {
    // Validate account
    account, err := getAccount(ctx, payment.AccountID)
    if err != nil {
        return fmt.Errorf("failed to get account: %w", err)
    }
    
    // Check balance
    if account.Balance < payment.Amount {
        return commons.ValidateBusinessError(
            constant.ErrInsufficientFunds,
            "Payment",
            account.ID,
            payment.Amount,
            account.Balance,
        )
    }
    
    // Process payment
    err = debitAccount(ctx, account, payment.Amount)
    if err != nil {
        return fmt.Errorf("failed to debit account: %w", err)
    }
    
    return nil
}
```

## Error Handling Patterns

### Repository Layer

```go
type AccountRepository struct {
    db *sql.DB
}

func (r *AccountRepository) GetByID(ctx context.Context, id string) (*Account, error) {
    var account Account
    err := r.db.QueryRowContext(ctx, 
        "SELECT id, balance, status FROM accounts WHERE id = $1", 
        id,
    ).Scan(&account.ID, &account.Balance, &account.Status)
    
    if err == sql.ErrNoRows {
        return nil, commons.ValidateBusinessError(
            constant.ErrAccountNotFound,
            "Account",
            id,
        )
    }
    
    if err != nil {
        return nil, fmt.Errorf("database error: %w", err)
    }
    
    return &account, nil
}
```

### Service Layer

```go
type TransactionService struct {
    accountRepo AccountRepository
    logger      commons.Logger
}

func (s *TransactionService) Transfer(ctx context.Context, req TransferRequest) error {
    logger := commons.NewLoggerFromContext(ctx)
    
    // Validate request
    if req.Amount <= 0 {
        return commons.ValidateBusinessError(
            constant.ErrInvalidInput,
            "TransferRequest",
            "amount must be positive",
        )
    }
    
    // Get source account
    sourceAccount, err := s.accountRepo.GetByID(ctx, req.SourceAccountID)
    if err != nil {
        logger.Error("Failed to get source account", "error", err)
        return err // Business error already formatted
    }
    
    // Check eligibility
    if sourceAccount.Status != "ACTIVE" {
        return commons.ValidateBusinessError(
            constant.ErrAccountIneligibility,
            "Account",
            sourceAccount.ID,
            "account is not active",
        )
    }
    
    return nil
}
```

## Best Practices

1. **Use error constants**: Define error codes as constants for consistency
2. **Include context**: Provide meaningful error messages with context
3. **Log at the source**: Log errors where they occur, not where they're handled
4. **Wrap errors**: Use `fmt.Errorf` with `%w` to maintain error chain
5. **Business vs technical**: Distinguish between business errors and technical errors

## Error Code Guidelines

- **0xxx**: Account-related errors
- **1xxx**: Transaction-related errors
- **2xxx**: Asset-related errors
- **3xxx**: Ledger-related errors
- **9xxx**: General/system errors

## Internationalization

The error handling system supports internationalization through message templates:

```go
// Define message templates
var errorMessages = map[string]map[string]string{
    "en": {
        "0001": "Account %s not found",
        "0002": "Insufficient funds in account %s. Required: %v, Available: %v",
    },
    "pt": {
        "0001": "Conta %s não encontrada",
        "0002": "Saldo insuficiente na conta %s. Necessário: %v, Disponível: %v",
    },
}

// Use with locale
func ValidateBusinessErrorWithLocale(err error, locale, entityType string, args ...interface{}) Response {
    // Implementation would look up the appropriate message template
    // based on the error code and locale
}
```