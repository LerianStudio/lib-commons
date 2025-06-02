package transaction

import (
	"math"
	"testing"
	"time"

	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/stretchr/testify/assert"
)

func TestUpdateBalances(t *testing.T) {
	tests := []struct {
		name         string
		operation    string
		fromTo       map[string]Amount
		balances     []*Balance
		wantBalances []*Balance
		wantErr      bool
	}{
		{
			name:      "update balances with debit",
			operation: constant.DEBIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 50, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:             "id1",
					Alias:          "acc1",
					OrganizationID: "org1",
					LedgerID:       "ledger1",
					AssetCode:      "USD",
					Available:      100,
					Scale:          2,
					OnHold:         10,
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "standard",
					Version:        1,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
				},
			},
			wantBalances: []*Balance{
				{
					ID:             "id1",
					Alias:          "acc1",
					OrganizationID: "org1",
					LedgerID:       "ledger1",
					AssetCode:      "USD",
					Available:      50,
					Scale:          2,
					OnHold:         10,
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "standard",
					Version:        1,
				},
			},
			wantErr: false,
		},
		{
			name:      "update balances with credit",
			operation: constant.CREDIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 50, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 100,
					Scale:     2,
					OnHold:    10,
				},
			},
			wantBalances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 150,
					Scale:     2,
					OnHold:    10,
				},
			},
			wantErr: false,
		},
		{
			name:      "update multiple balances",
			operation: constant.CREDIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 50, Scale: 2},
				"acc2": {Value: 100, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 100,
					Scale:     2,
				},
				{
					ID:        "id2",
					Alias:     "acc2",
					Available: 200,
					Scale:     2,
				},
			},
			wantBalances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 150,
					Scale:     2,
				},
				{
					ID:        "id2",
					Alias:     "acc2",
					Available: 300,
					Scale:     2,
				},
			},
			wantErr: false,
		},
		{
			name:      "overflow error",
			operation: constant.CREDIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 2, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: math.MaxInt64 - 1,
					Scale:     2,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultChan := make(chan []*Balance, 1)
			errChan := make(chan error, 1)

			go UpdateBalances(tt.operation, tt.fromTo, tt.balances, resultChan, errChan)

			select {
			case err := <-errChan:
				if !tt.wantErr {
					t.Errorf("unexpected error: %v", err)
				}
			case result := <-resultChan:
				if tt.wantErr {
					t.Error("expected error but got none")
				} else {
					assert.Len(t, result, len(tt.wantBalances))
					for i, bal := range result {
						assert.Equal(t, tt.wantBalances[i].ID, bal.ID)
						assert.Equal(t, tt.wantBalances[i].Alias, bal.Alias)
						assert.Equal(t, tt.wantBalances[i].Available, bal.Available)
						assert.Equal(t, tt.wantBalances[i].Scale, bal.Scale)
						assert.Equal(t, tt.wantBalances[i].OnHold, bal.OnHold)
					}
				}
			case <-time.After(1 * time.Second):
				t.Error("test timed out")
			}
		})
	}
}

func TestAppendIfNotExist(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		toAppend []string
		want     []string
	}{
		{
			name:     "append new elements",
			slice:    []string{"a", "b"},
			toAppend: []string{"c", "d"},
			want:     []string{"a", "b", "c", "d"},
		},
		{
			name:     "append with duplicates",
			slice:    []string{"a", "b", "c"},
			toAppend: []string{"b", "d", "c", "e"},
			want:     []string{"a", "b", "c", "d", "e"},
		},
		{
			name:     "append to empty slice",
			slice:    []string{},
			toAppend: []string{"a", "b"},
			want:     []string{"a", "b"},
		},
		{
			name:     "append empty slice",
			slice:    []string{"a", "b"},
			toAppend: []string{},
			want:     []string{"a", "b"},
		},
		{
			name:     "all duplicates",
			slice:    []string{"a", "b", "c"},
			toAppend: []string{"a", "b", "c"},
			want:     []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppendIfNotExist(tt.slice, tt.toAppend)
			assert.Equal(t, tt.want, got)
		})
	}
}
