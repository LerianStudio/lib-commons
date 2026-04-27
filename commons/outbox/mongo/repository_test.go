//go:build unit

package mongo

import (
	"context"
	"database/sql"
	"testing"
	"time"

	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewRepository_Validation(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(nil)
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrConnectionRequired)

	repo, err = NewRepository(&libMongo.Client{}, WithCollectionName("bad-name"))
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrInvalidIdentifier)

	repo, err = NewRepository(&libMongo.Client{}, WithTenantField("bad-name"))
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrInvalidIdentifier)
}

func TestRepository_CreateWithTxUnsupported(t *testing.T) {
	t.Parallel()

	repo := &Repository{}
	_, err := repo.CreateWithTx(context.Background(), &sql.Tx{}, nil)
	require.ErrorIs(t, err, ErrTransactionUnsupported)
}

func TestRepository_TenantIDFromContext(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true, tenantField: defaultTenantField}

	_, err := repo.tenantIDFromContext(context.Background())
	require.ErrorIs(t, err, outbox.ErrTenantIDRequired)

	ctx := outbox.ContextWithTenantID(context.Background(), "tenant-a")
	tenantID, err := repo.tenantIDFromContext(ctx)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", tenantID)

	repo.requireTenant = false
	tenantID, err = repo.tenantIDFromContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultScopeTenantID, tenantID)
}

func TestDocumentRoundTrip_CustomTenantField(t *testing.T) {
	t.Parallel()

	publishedAt := time.Now().UTC().Truncate(time.Millisecond)
	doc := document{
		ID:          uuid.NewString(),
		EventType:   "payment.created",
		AggregateID: uuid.NewString(),
		Payload:     `{"ok":true}`,
		Status:      outbox.OutboxStatusPending,
		Attempts:    2,
		PublishedAt: &publishedAt,
		LastError:   "failure",
		CreatedAt:   publishedAt,
		UpdatedAt:   publishedAt,
		TenantID:    "tenant-a",
	}

	raw := doc.toBSON("scope")
	decoded, err := documentFromBSON(raw, "scope")
	require.NoError(t, err)
	require.Equal(t, doc, decoded)
}
