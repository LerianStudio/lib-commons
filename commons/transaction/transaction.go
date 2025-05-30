// Package transaction provides data structures and utilities for financial transaction processing.
// It includes models for transactions, balances, amounts, and validation functions.
package transaction

import (
	"strconv"
	"strings"
	"time"
)

// Balance structure for marshaling/unmarshalling JSON.
//
// swagger:model Balance
// @Description Balance is the struct designed to represent the account balance.
type Balance struct {
	ID             string         `json:"id" example:"00000000-0000-0000-0000-000000000000"`
	OrganizationID string         `json:"organizationId" example:"00000000-0000-0000-0000-000000000000"`
	LedgerID       string         `json:"ledgerId" example:"00000000-0000-0000-0000-000000000000"`
	AccountID      string         `json:"accountId" example:"00000000-0000-0000-0000-000000000000"`
	Alias          string         `json:"alias" example:"@person1"`
	AssetCode      string         `json:"assetCode" example:"BRL"`
	Available      int64          `json:"available" example:"1500"`
	OnHold         int64          `json:"onHold" example:"500"`
	Scale          int64          `json:"scale" example:"2"`
	Version        int64          `json:"version" example:"1"`
	AccountType    string         `json:"accountType" example:"creditCard"`
	AllowSending   bool           `json:"allowSending" example:"true"`
	AllowReceiving bool           `json:"allowReceiving" example:"true"`
	CreatedAt      time.Time      `json:"createdAt" example:"2021-01-01T00:00:00Z"`
	UpdatedAt      time.Time      `json:"updatedAt" example:"2021-01-01T00:00:00Z"`
	DeletedAt      *time.Time     `json:"deletedAt" example:"2021-01-01T00:00:00Z"`
	Metadata       map[string]any `json:"metadata,omitempty"`
} // @name Balance

// Responses represents the aggregated response data for transaction operations.
type Responses struct {
	Total        int64
	Asset        string
	From         map[string]Amount
	To           map[string]Amount
	Sources      []string
	Destinations []string
	Aliases      []string
}

// Metadata structure for marshaling/unmarshalling JSON.
//
// swagger:model Metadata
// @Description Metadata is the struct designed to store metadata.
type Metadata struct {
	Key   string `json:"key,omitempty"`
	Value any    `json:"value,omitempty"`
} // @name Metadata

// Amount structure for marshaling/unmarshalling JSON.
//
// swagger:model Amount
// @Description Amount is the struct designed to represent the amount of an operation.
type Amount struct {
	Asset     string `json:"asset,omitempty" validate:"required" example:"BRL"`
	Value     int64  `json:"value,omitempty" validate:"required" example:"1000"`
	Scale     int64  `json:"scale,omitempty" validate:"gte=0" example:"2"`
	Operation string `json:"operation,omitempty"`
} // @name Amount

// Share structure for marshaling/unmarshalling JSON.
//
// swagger:model Share
// @Description Share is the struct designed to represent the sharing fields of an operation.
type Share struct {
	Percentage             int64 `json:"percentage,omitempty" validate:"required"`
	PercentageOfPercentage int64 `json:"percentageOfPercentage,omitempty"`
} // @name Share

// Send structure for marshaling/unmarshalling JSON.
//
// swagger:model Send
// @Description Send is the struct designed to represent the sending fields of an operation.
type Send struct {
	Asset      string     `json:"asset,omitempty" validate:"required" example:"BRL"`
	Value      int64      `json:"value,omitempty" validate:"required" example:"1000"`
	Scale      int64      `json:"scale,omitempty" validate:"gte=0" example:"2"`
	Source     Source     `json:"source,omitempty" validate:"required"`
	Distribute Distribute `json:"distribute,omitempty" validate:"required"`
} // @name Send

// Source structure for marshaling/unmarshalling JSON.
//
// swagger:model Source
// @Description Source is the struct designed to represent the source fields of an operation.
type Source struct {
	Remaining string   `json:"remaining,omitempty" example:"remaining"`
	From      []FromTo `json:"from,omitempty" validate:"singletransactiontype,required,dive"`
} // @name Source

// Rate structure for marshaling/unmarshalling JSON.
//
// swagger:model Rate
// @Description Rate is the struct designed to represent the rate fields of an operation.
type Rate struct {
	From       string `json:"from" validate:"required" example:"BRL"`
	To         string `json:"to" validate:"required" example:"USDe"`
	Value      int64  `json:"value" validate:"required" example:"1000"`
	Scale      int64  `json:"scale" validate:"gte=0" example:"2"`
	ExternalID string `json:"externalId" validate:"uuid,required" example:"00000000-0000-0000-0000-000000000000"`
} // @name Rate

// IsEmpty method that set empty or nil in fields
func (r Rate) IsEmpty() bool {
	return r.ExternalID == "" && r.From == "" && r.To == "" && r.Value == 0
}

// FromTo structure for marshaling/unmarshalling JSON.
//
// swagger:model FromTo
// @Description FromTo is the struct designed to represent the from/to fields of an operation.
type FromTo struct {
	Account         string         `json:"account,omitempty" example:"@person1"`
	AccountAlias    string         `json:"accountAlias,omitempty" example:"@person1"`
	Amount          *Amount        `json:"amount,omitempty"`
	Share           *Share         `json:"share,omitempty"`
	Remaining       string         `json:"remaining,omitempty" example:"remaining"`
	Rate            *Rate          `json:"rate,omitempty"`
	Description     string         `json:"description,omitempty" example:"description"`
	ChartOfAccounts string         `json:"chartOfAccounts" example:"1000"`
	Metadata        map[string]any `json:"metadata" validate:"dive,keys,keymax=100,endkeys,nonested,valuemax=2000"`
	IsFrom          bool           `json:"isFrom,omitempty" example:"true"`
} // @name FromTo

// SplitAlias function to split alias with index.
func (ft FromTo) SplitAlias() string {
	if ft.Account != "" {
		if strings.Contains(ft.Account, "#") {
			parts := strings.SplitN(ft.Account, "#", 2)
			if len(parts) > 1 {
				return parts[1]
			}
		}

		return ft.Account
	}

	if strings.Contains(ft.AccountAlias, "#") {
		parts := strings.SplitN(ft.AccountAlias, "#", 2)
		if len(parts) > 1 {
			return parts[1]
		}
	}

	return ft.AccountAlias
}

// ConcatAlias function to concat alias with index.
func (ft FromTo) ConcatAlias(i int) string {
	if ft.Account != "" {
		return strconv.Itoa(i) + "#" + ft.Account
	}

	return strconv.Itoa(i) + "#" + ft.AccountAlias
}

// Distribute structure for marshaling/unmarshalling JSON.
//
// swagger:model Distribute
// @Description Distribute is the struct designed to represent the distribution fields of an operation.
type Distribute struct {
	Remaining string   `json:"remaining,omitempty"`
	To        []FromTo `json:"to,omitempty" validate:"singletransactiontype,required,dive"`
} // @name Distribute

// Transaction structure for marshaling/unmarshalling JSON.
//
// swagger:model Transaction
// @Description Transaction is a struct designed to store transaction data.
type Transaction struct {
	ChartOfAccountsGroupName string         `json:"chartOfAccountsGroupName,omitempty" example:"1000"`
	Description              string         `json:"description,omitempty" example:"Description"`
	Code                     string         `json:"code,omitempty" example:"00000000-0000-0000-0000-000000000000"`
	Pending                  bool           `json:"pending,omitempty" example:"false"`
	Metadata                 map[string]any `json:"metadata,omitempty" validate:"dive,keys,keymax=100,endkeys,nonested,valuemax=2000"`
	Send                     Send           `json:"send" validate:"required"`
} // @name Transaction

// IsEmpty is a func that validate if transaction is Empty.
func (t Transaction) IsEmpty() bool {
	return t.Send.Asset == "" && t.Send.Value == 0 && t.Send.Scale == 0
}
