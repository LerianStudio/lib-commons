package mongo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
)

func (doc document) toBSON(tenantField string) bson.M {
	raw := bson.M{
		"id":                doc.ID,
		"event_type":        doc.EventType,
		"aggregate_id":      doc.AggregateID,
		"payload":           doc.Payload,
		mongoFieldStatus:    doc.Status,
		mongoFieldAttempts:  doc.Attempts,
		mongoFieldCreatedAt: doc.CreatedAt,
		mongoFieldUpdatedAt: doc.UpdatedAt,
	}

	if doc.PublishedAt != nil {
		raw["published_at"] = *doc.PublishedAt
	}

	if doc.LastError != "" {
		raw[mongoFieldLastError] = doc.LastError
	}

	if tenantField != "" {
		raw[tenantField] = doc.TenantID
	}

	return raw
}

func (repo *Repository) decodeSingle(ctx context.Context, collection *mongodriver.Collection, filter bson.M) (document, error) {
	if repo.tenantField == defaultTenantField {
		var doc document
		if err := collection.FindOne(ctx, filter).Decode(&doc); err != nil {
			return document{}, err
		}

		return validateDecodedDocument(doc)
	}

	var raw bson.M
	if err := collection.FindOne(ctx, filter).Decode(&raw); err != nil {
		return document{}, err
	}

	return documentFromBSON(raw, repo.tenantField)
}

func (repo *Repository) decodeCursor(ctx context.Context, cursor *mongodriver.Cursor, capacity int) ([]document, error) {
	docs := make([]document, 0, capacity)

	for cursor.Next(ctx) {
		var doc document
		if repo.tenantField == defaultTenantField {
			if err := cursor.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode outbox event: %w", err)
			}

			validated, err := validateDecodedDocument(doc)
			if err != nil {
				return nil, fmt.Errorf("decode outbox event: %w", err)
			}

			docs = append(docs, validated)

			continue
		}

		var raw bson.M
		if err := cursor.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode outbox event: %w", err)
		}

		decoded, err := documentFromBSON(raw, repo.tenantField)
		if err != nil {
			return nil, fmt.Errorf("decode outbox event: %w", err)
		}

		docs = append(docs, decoded)
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return docs, nil
}

func (repo *Repository) decodeAndCloseCursor(ctx context.Context, cursor *mongodriver.Cursor, capacity int) (docs []document, err error) {
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), cursorCloseTimeout)
		defer cancel()

		if closeErr := cursor.Close(closeCtx); closeErr != nil && err == nil {
			err = fmt.Errorf("close mongo cursor: %w", closeErr)
		}
	}()

	return repo.decodeCursor(ctx, cursor, capacity)
}

func (repo *Repository) decodeClaimCursor(ctx context.Context, cursor *mongodriver.Cursor, capacity int) ([]document, error) {
	docs := make([]document, 0, capacity)

	for cursor.Next(ctx) {
		if repo.tenantField == defaultTenantField {
			var doc document
			if err := cursor.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode outbox claim candidate: %w", err)
			}

			validated, err := validateClaimDocument(doc)
			if err != nil {
				return nil, fmt.Errorf("decode outbox claim candidate: %w", err)
			}

			docs = append(docs, validated)

			continue
		}

		var raw bson.M
		if err := cursor.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode outbox claim candidate: %w", err)
		}

		decoded, err := claimDocumentFromBSON(raw, repo.tenantField)
		if err != nil {
			return nil, fmt.Errorf("decode outbox claim candidate: %w", err)
		}

		docs = append(docs, decoded)
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return docs, nil
}

func (repo *Repository) decodeAndCloseClaimCursor(ctx context.Context, cursor *mongodriver.Cursor, capacity int) (docs []document, err error) {
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), cursorCloseTimeout)
		defer cancel()

		if closeErr := cursor.Close(closeCtx); closeErr != nil && err == nil {
			err = fmt.Errorf("close mongo cursor: %w", closeErr)
		}
	}()

	return repo.decodeClaimCursor(ctx, cursor, capacity)
}

func documentFromBSON(raw bson.M, tenantField string) (document, error) {
	id, err := stringField(raw, "id")
	if err != nil {
		return document{}, err
	}

	eventType, err := stringField(raw, "event_type")
	if err != nil {
		return document{}, err
	}

	aggregateID, err := stringField(raw, "aggregate_id")
	if err != nil {
		return document{}, err
	}

	payload, err := stringField(raw, "payload")
	if err != nil {
		return document{}, err
	}

	status, err := stringField(raw, mongoFieldStatus)
	if err != nil {
		return document{}, err
	}

	attempts, err := intField(raw, mongoFieldAttempts)
	if err != nil {
		return document{}, err
	}

	createdAt, err := timeField(raw, mongoFieldCreatedAt)
	if err != nil {
		return document{}, err
	}

	updatedAt, err := timeField(raw, mongoFieldUpdatedAt)
	if err != nil {
		return document{}, err
	}

	lastError, err := optionalStringField(raw, mongoFieldLastError)
	if err != nil {
		return document{}, err
	}

	publishedAt, err := optionalTimeField(raw, "published_at")
	if err != nil {
		return document{}, err
	}

	tenantID := defaultScopeTenantID
	if tenantField != "" {
		tenantID, err = optionalStringField(raw, tenantField)
		if err != nil {
			return document{}, err
		}
	}

	return validateDecodedDocument(document{
		ID:          id,
		EventType:   eventType,
		AggregateID: aggregateID,
		Payload:     payload,
		Status:      status,
		Attempts:    attempts,
		PublishedAt: publishedAt,
		LastError:   strings.TrimSpace(lastError),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		TenantID:    strings.TrimSpace(tenantID),
	})
}

func validateDecodedDocument(doc document) (document, error) {
	doc.Status = strings.TrimSpace(doc.Status)
	if _, err := outbox.ParseOutboxEventStatus(doc.Status); err != nil {
		return document{}, err
	}

	if doc.Attempts < 0 {
		return document{}, fmt.Errorf("field %q must be non-negative", mongoFieldAttempts)
	}

	doc.LastError = strings.TrimSpace(doc.LastError)
	doc.TenantID = strings.TrimSpace(doc.TenantID)

	return doc, nil
}

func claimDocumentFromBSON(raw bson.M, tenantField string) (document, error) {
	id, err := stringField(raw, "id")
	if err != nil {
		return document{}, err
	}

	status, err := stringField(raw, mongoFieldStatus)
	if err != nil {
		return document{}, err
	}

	attempts, err := intField(raw, mongoFieldAttempts)
	if err != nil {
		return document{}, err
	}

	updatedAt, err := timeField(raw, mongoFieldUpdatedAt)
	if err != nil {
		return document{}, err
	}

	lastError, err := optionalStringField(raw, mongoFieldLastError)
	if err != nil {
		return document{}, err
	}

	tenantID := defaultScopeTenantID
	if tenantField != "" {
		tenantID, err = optionalStringField(raw, tenantField)
		if err != nil {
			return document{}, err
		}
	}

	return validateClaimDocument(document{
		ID:        id,
		Status:    status,
		Attempts:  attempts,
		LastError: strings.TrimSpace(lastError),
		UpdatedAt: updatedAt,
		TenantID:  strings.TrimSpace(tenantID),
	})
}

func validateClaimDocument(doc document) (document, error) {
	doc.ID = strings.TrimSpace(doc.ID)
	if doc.ID == "" {
		return document{}, fmt.Errorf("missing field %q", "id")
	}

	doc.Status = strings.TrimSpace(doc.Status)
	if _, err := outbox.ParseOutboxEventStatus(doc.Status); err != nil {
		return document{}, err
	}

	if doc.Attempts < 0 {
		return document{}, fmt.Errorf("field %q must be non-negative", mongoFieldAttempts)
	}

	if doc.UpdatedAt.IsZero() {
		return document{}, fmt.Errorf("missing field %q", mongoFieldUpdatedAt)
	}

	doc.LastError = strings.TrimSpace(doc.LastError)
	doc.TenantID = strings.TrimSpace(doc.TenantID)

	return doc, nil
}

func docsToEvents(docs []document) ([]*outbox.OutboxEvent, error) {
	result := make([]*outbox.OutboxEvent, 0, len(docs))
	for _, doc := range docs {
		event, err := doc.toOutboxEvent()
		if err != nil {
			return nil, err
		}

		result = append(result, event)
	}

	return result, nil
}

func (doc document) toOutboxEvent() (*outbox.OutboxEvent, error) {
	id, err := uuid.Parse(doc.ID)
	if err != nil {
		return nil, fmt.Errorf("parse outbox event id: %w", err)
	}

	aggregateID, err := uuid.Parse(doc.AggregateID)
	if err != nil {
		return nil, fmt.Errorf("parse outbox aggregate id: %w", err)
	}

	return &outbox.OutboxEvent{
		ID:          id,
		EventType:   doc.EventType,
		AggregateID: aggregateID,
		Payload:     []byte(doc.Payload),
		Status:      doc.Status,
		Attempts:    doc.Attempts,
		PublishedAt: doc.PublishedAt,
		LastError:   doc.LastError,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}, nil
}

func stringField(raw bson.M, key string) (string, error) {
	value, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("missing field %q", key)
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q must be string", key)
	}

	return str, nil
}

func optionalStringField(raw bson.M, key string) (string, error) {
	value, ok := raw[key]
	if !ok || value == nil {
		return "", nil
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q must be string", key)
	}

	return str, nil
}

func intField(raw bson.M, key string) (int, error) {
	value, ok := raw[key]
	if !ok {
		return 0, fmt.Errorf("missing field %q", key)
	}

	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	default:
		return 0, fmt.Errorf("field %q must be integer", key)
	}
}

func timeField(raw bson.M, key string) (time.Time, error) {
	value, ok := raw[key]
	if !ok {
		return time.Time{}, fmt.Errorf("missing field %q", key)
	}

	switch typed := value.(type) {
	case time.Time:
		return typed, nil
	case primitive.DateTime:
		return typed.Time(), nil
	default:
		return time.Time{}, fmt.Errorf("field %q must be time", key)
	}
}

func optionalTimeField(raw bson.M, key string) (*time.Time, error) {
	value, ok := raw[key]
	if !ok || value == nil {
		return nil, nil //nolint:nilnil // nil pointer represents an absent optional timestamp.
	}

	switch typed := value.(type) {
	case time.Time:
		return &typed, nil
	case primitive.DateTime:
		timeValue := typed.Time()
		return &timeValue, nil
	default:
		return nil, fmt.Errorf("field %q must be time", key)
	}
}

type createValues struct {
	id          uuid.UUID
	eventType   string
	aggregateID uuid.UUID
	payload     []byte
	status      string
	attempts    int
	publishedAt *time.Time
	lastError   string
	createdAt   time.Time
	updatedAt   time.Time
}

func documentFromCreateValues(values createValues, tenantID string) document {
	return document{
		ID:          values.id.String(),
		EventType:   values.eventType,
		AggregateID: values.aggregateID.String(),
		Payload:     string(values.payload),
		Status:      values.status,
		Attempts:    values.attempts,
		PublishedAt: values.publishedAt,
		LastError:   values.lastError,
		CreatedAt:   values.createdAt,
		UpdatedAt:   values.updatedAt,
		TenantID:    tenantID,
	}
}

func normalizedCreateValues(event *outbox.OutboxEvent, now time.Time) createValues {
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	updatedAt := event.UpdatedAt
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}

	return createValues{
		id:          event.ID,
		eventType:   strings.TrimSpace(event.EventType),
		aggregateID: event.AggregateID,
		payload:     event.Payload,
		status:      outbox.OutboxStatusPending,
		attempts:    0,
		publishedAt: nil,
		lastError:   "",
		createdAt:   createdAt,
		updatedAt:   updatedAt,
	}
}

func validateCreateEvent(event *outbox.OutboxEvent) error {
	if event == nil {
		return outbox.ErrOutboxEventRequired
	}

	if event.ID == uuid.Nil {
		return ErrIDRequired
	}

	if strings.TrimSpace(event.EventType) == "" {
		return ErrEventTypeRequired
	}

	if event.AggregateID == uuid.Nil {
		return ErrAggregateIDRequired
	}

	if len(event.Payload) == 0 {
		return outbox.ErrOutboxEventPayloadRequired
	}

	if len(event.Payload) > outbox.DefaultMaxPayloadBytes {
		return outbox.ErrOutboxEventPayloadTooLarge
	}

	if !json.Valid(event.Payload) {
		return outbox.ErrOutboxEventPayloadNotJSON
	}

	return nil
}
