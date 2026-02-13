// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package constant

import "errors"

var (
	ErrInsufficientFunds                   = errors.New("0018")
	ErrAccountIneligibility                = errors.New("0019")
	ErrAccountStatusTransactionRestriction = errors.New("0024")
	ErrAssetCodeNotFound                   = errors.New("0034")
	ErrTransactionValueMismatch            = errors.New("0073")
	ErrTransactionAmbiguous                = errors.New("0090")
	ErrOverFlowInt64                       = errors.New("0097")
	ErrOnHoldExternalAccount               = errors.New("0098")
)
