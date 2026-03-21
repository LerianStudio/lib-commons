//go:build unit

package outbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOutboxEventStatus(t *testing.T) {
	t.Parallel()

	status, err := ParseOutboxEventStatus(OutboxStatusPending)
	require.NoError(t, err)
	require.Equal(t, StatusPending, status)

	_, err = ParseOutboxEventStatus("UNKNOWN")
	require.ErrorIs(t, err, ErrOutboxStatusInvalid)
}

func TestOutboxEventStatus_IsValid(t *testing.T) {
	t.Parallel()

	require.True(t, StatusPending.IsValid())
	require.True(t, StatusProcessing.IsValid())
	require.True(t, StatusPublished.IsValid())
	require.True(t, StatusFailed.IsValid())
	require.True(t, StatusInvalid.IsValid())
	require.False(t, OutboxEventStatus("BROKEN").IsValid())
}

func TestOutboxEventStatus_String(t *testing.T) {
	t.Parallel()

	require.Equal(t, OutboxStatusProcessing, StatusProcessing.String())
}

func TestOutboxEventStatus_CanTransitionTo(t *testing.T) {
	t.Parallel()

	require.True(t, StatusPending.CanTransitionTo(StatusProcessing))
	require.True(t, StatusProcessing.CanTransitionTo(StatusPublished))
	require.False(t, StatusPublished.CanTransitionTo(StatusProcessing))
}

func TestValidateOutboxTransition(t *testing.T) {
	t.Parallel()

	// Valid transitions.
	require.NoError(t, ValidateOutboxTransition(OutboxStatusPending, OutboxStatusProcessing))
	require.NoError(t, ValidateOutboxTransition(OutboxStatusFailed, OutboxStatusProcessing))
	require.NoError(t, ValidateOutboxTransition(OutboxStatusProcessing, OutboxStatusPublished))
	require.NoError(t, ValidateOutboxTransition(OutboxStatusProcessing, OutboxStatusFailed))
	require.NoError(t, ValidateOutboxTransition(OutboxStatusProcessing, OutboxStatusInvalid))
	require.NoError(t, ValidateOutboxTransition(OutboxStatusProcessing, OutboxStatusProcessing))

	// Invalid transitions from terminal states.
	err := ValidateOutboxTransition(OutboxStatusPublished, OutboxStatusProcessing)
	require.ErrorIs(t, err, ErrOutboxTransitionInvalid)

	err = ValidateOutboxTransition(OutboxStatusPublished, OutboxStatusFailed)
	require.ErrorIs(t, err, ErrOutboxTransitionInvalid)

	err = ValidateOutboxTransition(OutboxStatusInvalid, OutboxStatusProcessing)
	require.ErrorIs(t, err, ErrOutboxTransitionInvalid)

	err = ValidateOutboxTransition(OutboxStatusInvalid, OutboxStatusPending)
	require.ErrorIs(t, err, ErrOutboxTransitionInvalid)

	// Invalid backward transitions.
	err = ValidateOutboxTransition(OutboxStatusPending, OutboxStatusFailed)
	require.ErrorIs(t, err, ErrOutboxTransitionInvalid)

	err = ValidateOutboxTransition(OutboxStatusFailed, OutboxStatusPublished)
	require.ErrorIs(t, err, ErrOutboxTransitionInvalid)

	// Unknown status.
	err = ValidateOutboxTransition("UNKNOWN", OutboxStatusProcessing)
	require.ErrorIs(t, err, ErrOutboxStatusInvalid)

	err = ValidateOutboxTransition(OutboxStatusProcessing, "BOGUS")
	require.ErrorIs(t, err, ErrOutboxStatusInvalid)
}
