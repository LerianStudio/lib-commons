//go:build unit

// Copyright 2025 Lerian Studio.

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownSequence_Execute(t *testing.T) {
	t.Parallel()

	errSupervisor := errors.New("supervisor failed")
	errBackend := errors.New("backend failed")

	tests := []struct {
		name          string
		allNil        bool
		supervisorErr error
		backendErr    error
		wantOrder     []string // expected call order recorded via append
		wantErr       bool
		checkErr      func(t *testing.T, err error)
	}{
		{
			name:      "all steps called in canonical order",
			wantOrder: []string{"prevent", "feed", "supervisor", "backend", "workers"},
		},
		{
			name:      "all nil steps do not panic",
			allNil:    true,
			wantOrder: nil,
			wantErr:   false,
		},
		{
			name:          "supervisor error is collected",
			supervisorErr: errSupervisor,
			wantOrder:     []string{"prevent", "feed", "supervisor", "backend", "workers"},
			wantErr:       true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errSupervisor)
				assert.ErrorContains(t, err, "stop supervisor")
			},
		},
		{
			name:       "backend error is collected",
			backendErr: errBackend,
			wantOrder:  []string{"prevent", "feed", "supervisor", "backend", "workers"},
			wantErr:    true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errBackend)
				assert.ErrorContains(t, err, "close backend")
			},
		},
		{
			name:          "both errors combined via errors.Join",
			supervisorErr: errSupervisor,
			backendErr:    errBackend,
			wantOrder:     []string{"prevent", "feed", "supervisor", "backend", "workers"},
			wantErr:       true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errSupervisor)
				assert.ErrorIs(t, err, errBackend)
				assert.ErrorContains(t, err, "stop supervisor")
				assert.ErrorContains(t, err, "close backend")
			},
		},
		{
			name:          "all steps run even when supervisor fails",
			supervisorErr: errSupervisor,
			wantOrder:     []string{"prevent", "feed", "supervisor", "backend", "workers"},
			wantErr:       true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errSupervisor)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var order []string

			seq := buildShutdownSequence(&order, tt.allNil, tt.supervisorErr, tt.backendErr)

			err := seq.Execute(context.Background())

			if tt.wantOrder != nil {
				assert.Equal(t, tt.wantOrder, order)
			}

			if tt.wantErr {
				require.Error(t, err)

				if tt.checkErr != nil {
					tt.checkErr(t, err)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func buildShutdownSequence(order *[]string, allNil bool, errSup, errBack error) ShutdownSequence {
	if allNil {
		return ShutdownSequence{}
	}

	seq := ShutdownSequence{
		PreventMutations: func() { *order = append(*order, "prevent") },
		CancelFeed:       func() { *order = append(*order, "feed") },
		StopSupervisor: func(_ context.Context) error {
			*order = append(*order, "supervisor")
			return nil
		},
		CloseBackend: func() error {
			*order = append(*order, "backend")
			return nil
		},
		StopWorkers: func() { *order = append(*order, "workers") },
	}

	if errSup != nil {
		seq.StopSupervisor = func(_ context.Context) error {
			*order = append(*order, "supervisor")
			return errSup
		}
	}

	if errBack != nil {
		seq.CloseBackend = func() error {
			*order = append(*order, "backend")
			return errBack
		}
	}

	return seq
}

func TestShutdownSequence_Execute_PartialNilSteps(t *testing.T) {
	t.Parallel()

	var order []string

	seq := ShutdownSequence{
		PreventMutations: nil, // skipped
		CancelFeed:       func() { order = append(order, "feed") },
		StopSupervisor:   nil, // skipped
		CloseBackend:     func() error { order = append(order, "backend"); return nil },
		StopWorkers:      nil, // skipped
	}

	err := seq.Execute(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, []string{"feed", "backend"}, order)
}

func TestShutdownSequence_Execute_NilReceiver(t *testing.T) {
	t.Parallel()

	var seq *ShutdownSequence
	assert.NoError(t, seq.Execute(context.Background()))
}

func TestShutdownSequence_Execute_NilContextUsesBackground(t *testing.T) {
	t.Parallel()

	called := false
	seq := ShutdownSequence{
		StopSupervisor: func(ctx context.Context) error {
			called = true
			require.NotNil(t, ctx)
			assert.NoError(t, ctx.Err())
			return nil
		},
	}

	assert.NoError(t, seq.Execute(nil))
	assert.True(t, called)
}

func TestShutdownSequence_Execute_PanicsDoNotAbortLaterSteps(t *testing.T) {
	t.Parallel()

	var order []string
	errBackend := errors.New("backend failed")

	seq := ShutdownSequence{
		PreventMutations: func() {
			order = append(order, "prevent")
			panic("boom")
		},
		CancelFeed: func() { order = append(order, "feed") },
		StopSupervisor: func(_ context.Context) error {
			order = append(order, "supervisor")
			return nil
		},
		CloseBackend: func() error {
			order = append(order, "backend")
			return errBackend
		},
		StopWorkers: func() { order = append(order, "workers") },
	}

	err := seq.Execute(context.Background())

	require.Error(t, err)
	assert.Equal(t, []string{"prevent", "feed", "supervisor", "backend", "workers"}, order)
	assert.ErrorContains(t, err, "prevent mutations panic")
	assert.ErrorContains(t, err, "close backend")
	assert.ErrorIs(t, err, errBackend)
}
