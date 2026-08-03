// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	minimumDefaultRetentionDays  int32 = 1827
	minimumDefaultRetentionYears int32 = 5
	objectLockTimestampPrecision       = time.Second
	retainedRecoveryTimeout            = 10 * time.Second
	retainedRecoveryMaxKeys      int32 = 2
	retainedRecoveryMaxPages           = 32
)

// RetentionMode identifies the immutable retention mode applied to an object version.
type RetentionMode string

// RetentionModeCompliance is the only retention mode accepted by RetainedStorage.
const RetentionModeCompliance RetentionMode = "COMPLIANCE"

var (
	// ErrRetainedBodyRequired reports a retained create without a body.
	ErrRetainedBodyRequired = errors.New("retained object body is required")
	// ErrInvalidRetention reports missing or non-COMPLIANCE retained-create settings.
	ErrInvalidRetention = errors.New("retention must use COMPLIANCE mode and an explicit retain-until time")
	// ErrVersionIDRequired reports an absent S3 object version identifier.
	ErrVersionIDRequired = errors.New("object version ID is required")
	// ErrVersionIDMismatch reports metadata for a version other than the requested version.
	ErrVersionIDMismatch = errors.New("object version ID does not match requested version")
	// ErrVersionMetadataRequired reports an absent S3 response or response body.
	ErrVersionMetadataRequired = errors.New("object version metadata is required")
	// ErrRetentionMetadataRequired reports incomplete Object Lock metadata for a version.
	ErrRetentionMetadataRequired = errors.New("object retention metadata is required")
	// ErrRetentionNotCompliance reports a retention mode weaker than COMPLIANCE.
	ErrRetentionNotCompliance = errors.New("object retention mode must be COMPLIANCE")
	// ErrObjectLockConfigurationRequired reports absent bucket Object Lock metadata.
	ErrObjectLockConfigurationRequired = errors.New("object lock configuration is required")
	// ErrObjectLockNotEnabled reports a bucket without Object Lock enabled.
	ErrObjectLockNotEnabled = errors.New("object lock must be enabled")
	// ErrDefaultRetentionRequired reports an absent or incomplete default retention rule.
	ErrDefaultRetentionRequired = errors.New("default object lock retention is required")
	// ErrRetentionTooShort reports a default retention shorter than five years.
	ErrRetentionTooShort = errors.New("default object lock retention must be at least five years")
	// ErrExpectedRetainedObjectRequired reports incomplete recovery expectations.
	ErrExpectedRetainedObjectRequired = errors.New("expected retained object metadata is required")
	// ErrRetainedVersionAmbiguous reports that one exact retained version cannot be selected safely.
	ErrRetainedVersionAmbiguous = errors.New("retained object version is ambiguous")
	// ErrRetainedMetadataMismatch reports that an existing version does not match caller expectations.
	ErrRetainedMetadataMismatch = errors.New("retained object metadata does not match expectation")
)

// Retention describes the immutable retention policy attached to one object version.
type Retention struct {
	Mode        RetentionMode
	RetainUntil time.Time
}

// ObjectMetadata describes one exact, immutable S3 object version.
type ObjectMetadata struct {
	VersionID     string
	ETag          string
	ContentType   string
	ContentLength int64
	LastModified  time.Time
	Retention     Retention
}

// ExpectedRetainedObject is the metadata a create-or-recover caller requires.
// Payload digest verification remains the caller's responsibility.
type ExpectedRetainedObject struct {
	ContentType   string
	ContentLength int64
	Retention     Retention
}

// RetainedStorageError is a telemetry-safe retained-storage operation failure.
// Error text never includes bucket, key, version, or upstream error text.
type RetainedStorageError struct {
	operation      string
	classification error
	cause          error
}

// Error returns a bounded message safe for logs, spans, and client wrappers.
func (e *RetainedStorageError) Error() string {
	return "retained storage " + e.operation + " failed"
}

// Operation identifies the failed retained-storage operation without object identifiers.
func (e *RetainedStorageError) Operation() string {
	return e.operation
}

// Unwrap preserves sentinel and underlying errors.Is/errors.As classifications.
func (e *RetainedStorageError) Unwrap() []error {
	errorsToUnwrap := make([]error, 0, 2)
	if e.classification != nil {
		errorsToUnwrap = append(errorsToUnwrap, e.classification)
	}

	if e.cause != nil && !errors.Is(e.cause, e.classification) {
		errorsToUnwrap = append(errorsToUnwrap, e.cause)
	}

	return errorsToUnwrap
}

// RetainedStorage stores and reads immutable, COMPLIANCE-retained S3 object versions.
// It deliberately exposes no delete operation or Object Lock bypass.
type RetainedStorage interface {
	// CreateRetained atomically creates a retained object and returns its exact version metadata.
	CreateRetained(ctx context.Context, key string, body io.Reader, contentType string, retention Retention) (ObjectMetadata, error)
	// DownloadVersion returns the bytes of the exact requested version.
	DownloadVersion(ctx context.Context, key, versionID string) (io.ReadCloser, error)
	// StatVersion returns metadata for the exact requested version.
	StatVersion(ctx context.Context, key, versionID string) (ObjectMetadata, error)
	// ValidateDefaultRetention requires an enabled COMPLIANCE bucket rule of at least five years.
	ValidateDefaultRetention(ctx context.Context) error
}

// RecoverableRetainedStorage extends RetainedStorage with deterministic retry recovery.
type RecoverableRetainedStorage interface {
	RetainedStorage
	// CreateOrRecoverRetained creates once or returns the sole exact retained version
	// when a prior or concurrent write already completed with matching metadata.
	CreateOrRecoverRetained(ctx context.Context, key string, body io.Reader, expected ExpectedRetainedObject) (ObjectMetadata, error)
}

// RetainedObjectAPI is the narrow S3 API required by RetainedStorage.
type RetainedObjectAPI interface {
	PutObject(ctx context.Context, params *awss3.PutObjectInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	HeadObject(ctx context.Context, params *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObjectLockConfiguration(ctx context.Context, params *awss3.GetObjectLockConfigurationInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectLockConfigurationOutput, error)
}

// RecoverableRetainedObjectAPI adds exact-version discovery for retry recovery.
type RecoverableRetainedObjectAPI interface {
	RetainedObjectAPI
	ListObjectVersions(ctx context.Context, params *awss3.ListObjectVersionsInput, optFns ...func(*awss3.Options)) (*awss3.ListObjectVersionsOutput, error)
}

type retainedStorage struct {
	client RetainedObjectAPI
	bucket string
}

type recoverableRetainedStorage struct {
	*retainedStorage
	versionLister RecoverableRetainedObjectAPI
}

// NewRetainedStorage constructs a tenant-scoped retained storage over a narrow S3 object API.
func NewRetainedStorage(client RetainedObjectAPI, bucket string) (RetainedStorage, error) {
	return newRetainedStorage(client, bucket)
}

// NewRecoverableRetainedStorage constructs retained storage with deterministic retry recovery.
func NewRecoverableRetainedStorage(client RecoverableRetainedObjectAPI, bucket string) (RecoverableRetainedStorage, error) {
	store, err := newRetainedStorage(client, bucket)
	if err != nil {
		return nil, err
	}

	return &recoverableRetainedStorage{
		retainedStorage: store,
		versionLister:   client,
	}, nil
}

func newRetainedStorage(client RetainedObjectAPI, bucket string) (*retainedStorage, error) {
	if isNilRetainedObjectAPI(client) {
		return nil, errors.New("s3 client must not be nil")
	}

	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, errors.New("bucket must not be empty")
	}

	return &retainedStorage{client: client, bucket: bucket}, nil
}

func (s *retainedStorage) CreateRetained(
	ctx context.Context,
	key string,
	body io.Reader,
	contentType string,
	retention Retention,
) (ObjectMetadata, error) {
	if body == nil {
		return ObjectMetadata{}, newRetainedStorageError("create", ErrRetainedBodyRequired, nil)
	}

	if retention.Mode != RetentionModeCompliance || retention.RetainUntil.IsZero() {
		return ObjectMetadata{}, newRetainedStorageError("create", ErrInvalidRetention, nil)
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return ObjectMetadata{}, newRetainedStorageError("create", nil, err)
	}

	retainUntil := canonicalObjectLockTime(retention.RetainUntil)

	input := &awss3.PutObjectInput{
		Bucket:                    &s.bucket,
		Key:                       &resolvedKey,
		Body:                      body,
		IfNoneMatch:               aws.String("*"),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: &retainUntil,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}

	output, err := s.client.PutObject(ctx, input)
	if err != nil {
		if isAlreadyExists(err) {
			return ObjectMetadata{}, newRetainedStorageError("create", ErrObjectAlreadyExists, err)
		}

		return ObjectMetadata{}, newRetainedStorageError("create", nil, err)
	}

	if output == nil || output.VersionId == nil || *output.VersionId == "" {
		return ObjectMetadata{}, newRetainedStorageError("create", ErrVersionIDRequired, nil)
	}

	return s.statResolvedVersion(ctx, "create", resolvedKey, *output.VersionId)
}

func (s *recoverableRetainedStorage) CreateOrRecoverRetained(
	ctx context.Context,
	key string,
	body io.Reader,
	expected ExpectedRetainedObject,
) (ObjectMetadata, error) {
	if err := validateExpectedRetainedObject(expected); err != nil {
		return ObjectMetadata{}, newRetainedStorageError("create-or-recover", err, nil)
	}

	if body != nil {
		buffered, bufferErr := bufferExpectedRetainedBody(body, expected.ContentLength)
		if bufferErr != nil {
			return ObjectMetadata{}, bufferErr
		}

		body = buffered
	}

	metadata, err := s.CreateRetained(ctx, key, body, expected.ContentType, expected.Retention)
	if err == nil {
		validated, validationErr := validateRecoveredMetadata(metadata, expected)
		if validationErr != nil {
			return ObjectMetadata{}, newRetainedStorageError("create-or-recover", validationErr, nil)
		}

		return validated, nil
	}

	if !errors.Is(err, ErrObjectAlreadyExists) && !isAmbiguousRetainedWrite(err) {
		return ObjectMetadata{}, err
	}

	resolvedKey, resolveErr := GetS3KeyStorageContext(ctx, key)
	if resolveErr != nil {
		return ObjectMetadata{}, newRetainedStorageError("create-or-recover", nil, resolveErr)
	}

	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), retainedRecoveryTimeout)
	defer cancel()

	metadata, recoveryErr := s.recoverRetainedVersion(recoveryCtx, resolvedKey)
	if recoveryErr != nil {
		return ObjectMetadata{}, newRetainedStorageError("create-or-recover", recoveryErr, err)
	}

	validated, validationErr := validateRecoveredMetadata(metadata, expected)
	if validationErr != nil {
		return ObjectMetadata{}, newRetainedStorageError("create-or-recover", validationErr, nil)
	}

	return validated, nil
}

// recoverRetainedVersion selects the sole exact retained version for resolvedKey.
// The versions listing is prefix-based, so sibling keys such as resolvedKey+".bak"
// can share pages with the exact key; listing paginates until every entry for the
// exact key has been evaluated and stops once the sorted listing passes it.
func (s *recoverableRetainedStorage) recoverRetainedVersion(ctx context.Context, resolvedKey string) (ObjectMetadata, error) {
	input := &awss3.ListObjectVersionsInput{
		Bucket:  &s.bucket,
		Prefix:  &resolvedKey,
		MaxKeys: aws.Int32(retainedRecoveryMaxKeys),
	}
	versionID := ""

	for page := 0; ; page++ {
		if page >= retainedRecoveryMaxPages {
			return ObjectMetadata{}, ErrRetainedVersionAmbiguous
		}

		output, err := s.versionLister.ListObjectVersions(ctx, input)
		if err != nil {
			return ObjectMetadata{}, fmt.Errorf("list retained object versions: %w", err)
		}

		if output == nil {
			return ObjectMetadata{}, ErrRetainedVersionAmbiguous
		}

		passedKey := false

		for _, version := range output.Versions {
			key := aws.ToString(version.Key)
			if key > resolvedKey {
				passedKey = true
				break
			}

			if key != resolvedKey {
				continue
			}

			if versionID != "" || !aws.ToBool(version.IsLatest) || aws.ToString(version.VersionId) == "" {
				return ObjectMetadata{}, ErrRetainedVersionAmbiguous
			}

			versionID = aws.ToString(version.VersionId)
		}

		for _, marker := range output.DeleteMarkers {
			key := aws.ToString(marker.Key)
			if key > resolvedKey {
				passedKey = true
				continue
			}

			if key == resolvedKey && aws.ToBool(marker.IsLatest) {
				return ObjectMetadata{}, ErrRetainedVersionAmbiguous
			}
		}

		if passedKey || !aws.ToBool(output.IsTruncated) {
			break
		}

		if output.NextKeyMarker == nil && output.NextVersionIdMarker == nil {
			return ObjectMetadata{}, ErrRetainedVersionAmbiguous
		}

		input.KeyMarker = output.NextKeyMarker
		input.VersionIdMarker = output.NextVersionIdMarker
	}

	if versionID == "" {
		return ObjectMetadata{}, ErrRetainedVersionAmbiguous
	}

	return s.statResolvedVersion(ctx, "stat", resolvedKey, versionID)
}

// bufferExpectedRetainedBody rejects a body whose length differs from the
// expectation before any immutable COMPLIANCE-retained write is issued.
func bufferExpectedRetainedBody(body io.Reader, expectedLength int64) (io.Reader, error) {
	content, err := io.ReadAll(io.LimitReader(body, expectedLength+1))
	if err != nil {
		return nil, newRetainedStorageError("create-or-recover", nil, fmt.Errorf("read retained object body: %w", err))
	}

	if int64(len(content)) != expectedLength {
		return nil, newRetainedStorageError("create-or-recover", ErrRetainedMetadataMismatch, nil)
	}

	return bytes.NewReader(content), nil
}

func validateExpectedRetainedObject(expected ExpectedRetainedObject) error {
	if expected.ContentType == "" || expected.ContentLength < 0 ||
		expected.Retention.Mode != RetentionModeCompliance || expected.Retention.RetainUntil.IsZero() {
		return ErrExpectedRetainedObjectRequired
	}

	return nil
}

func validateRecoveredMetadata(metadata ObjectMetadata, expected ExpectedRetainedObject) (ObjectMetadata, error) {
	expectedRetainUntil := canonicalObjectLockTime(expected.Retention.RetainUntil)
	if metadata.VersionID == "" || metadata.ContentType != expected.ContentType ||
		metadata.ContentLength != expected.ContentLength ||
		metadata.Retention.Mode != RetentionModeCompliance ||
		!metadata.Retention.RetainUntil.Equal(expectedRetainUntil) {
		return ObjectMetadata{}, ErrRetainedMetadataMismatch
	}

	return metadata, nil
}

func isAmbiguousRetainedWrite(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}

	var networkError net.Error

	return errors.As(err, &networkError) && networkError.Timeout()
}

func (s *retainedStorage) DownloadVersion(ctx context.Context, key, versionID string) (io.ReadCloser, error) {
	if versionID == "" {
		return nil, newRetainedStorageError("download", ErrVersionIDRequired, nil)
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return nil, newRetainedStorageError("download", nil, err)
	}

	output, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket:    &s.bucket,
		Key:       &resolvedKey,
		VersionId: &versionID,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, newRetainedStorageError("download", ErrObjectNotFound, err)
		}

		return nil, newRetainedStorageError("download", nil, err)
	}

	if output == nil || output.Body == nil {
		return nil, newRetainedStorageError("download", ErrVersionMetadataRequired, nil)
	}

	return output.Body, nil
}

func (s *retainedStorage) StatVersion(ctx context.Context, key, versionID string) (ObjectMetadata, error) {
	if versionID == "" {
		return ObjectMetadata{}, newRetainedStorageError("stat", ErrVersionIDRequired, nil)
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return ObjectMetadata{}, newRetainedStorageError("stat", nil, err)
	}

	return s.statResolvedVersion(ctx, "stat", resolvedKey, versionID)
}

func (s *retainedStorage) statResolvedVersion(ctx context.Context, operation, resolvedKey, versionID string) (ObjectMetadata, error) {
	output, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket:    &s.bucket,
		Key:       &resolvedKey,
		VersionId: &versionID,
	})
	if err != nil {
		if isNotFound(err) {
			return ObjectMetadata{}, newRetainedStorageError(operation, ErrObjectNotFound, err)
		}

		return ObjectMetadata{}, newRetainedStorageError(operation, nil, err)
	}

	metadata, metadataErr := metadataFromHead(output, versionID)
	if metadataErr != nil {
		return ObjectMetadata{}, newRetainedStorageError(operation, metadataErr, nil)
	}

	return metadata, nil
}

func metadataFromHead(output *awss3.HeadObjectOutput, requestedVersionID string) (ObjectMetadata, error) {
	if output == nil {
		return ObjectMetadata{}, ErrVersionMetadataRequired
	}

	versionID := aws.ToString(output.VersionId)
	if versionID == "" {
		return ObjectMetadata{}, ErrVersionIDRequired
	}

	if versionID != requestedVersionID {
		return ObjectMetadata{}, ErrVersionIDMismatch
	}

	if output.ObjectLockMode == "" || output.ObjectLockRetainUntilDate == nil {
		return ObjectMetadata{}, ErrRetentionMetadataRequired
	}

	if output.ObjectLockMode != s3types.ObjectLockModeCompliance {
		return ObjectMetadata{}, ErrRetentionNotCompliance
	}

	metadata := ObjectMetadata{
		VersionID:     versionID,
		ETag:          aws.ToString(output.ETag),
		ContentType:   aws.ToString(output.ContentType),
		ContentLength: aws.ToInt64(output.ContentLength),
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: canonicalObjectLockTime(*output.ObjectLockRetainUntilDate),
		},
	}
	if output.LastModified != nil {
		metadata.LastModified = output.LastModified.UTC()
	}

	return metadata, nil
}

func (s *retainedStorage) ValidateDefaultRetention(ctx context.Context) error {
	output, err := s.client.GetObjectLockConfiguration(ctx, &awss3.GetObjectLockConfigurationInput{
		Bucket: &s.bucket,
	})
	if err != nil {
		return newRetainedStorageError("validate-default-retention", nil, err)
	}

	if output == nil || output.ObjectLockConfiguration == nil {
		return newRetainedStorageError("validate-default-retention", ErrObjectLockConfigurationRequired, nil)
	}

	configuration := output.ObjectLockConfiguration
	if configuration.ObjectLockEnabled != s3types.ObjectLockEnabledEnabled {
		return newRetainedStorageError("validate-default-retention", ErrObjectLockNotEnabled, nil)
	}

	if configuration.Rule == nil || configuration.Rule.DefaultRetention == nil {
		return newRetainedStorageError("validate-default-retention", ErrDefaultRetentionRequired, nil)
	}

	defaultRetention := configuration.Rule.DefaultRetention
	if defaultRetention.Mode != s3types.ObjectLockRetentionModeCompliance {
		return newRetainedStorageError("validate-default-retention", ErrRetentionNotCompliance, nil)
	}

	if err := checkDefaultRetentionPeriod(defaultRetention); err != nil {
		return newRetainedStorageError("validate-default-retention", err, nil)
	}

	return nil
}

// checkDefaultRetentionPeriod requires exactly one period unit of at least five years.
func checkDefaultRetentionPeriod(retention *s3types.DefaultRetention) error {
	switch {
	case (retention.Years == nil) == (retention.Days == nil):
		return ErrDefaultRetentionRequired
	case retention.Years != nil && *retention.Years < minimumDefaultRetentionYears:
		return ErrRetentionTooShort
	case retention.Days != nil && *retention.Days < minimumDefaultRetentionDays:
		return ErrRetentionTooShort
	default:
		return nil
	}
}

func canonicalObjectLockTime(value time.Time) time.Time {
	return value.UTC().Truncate(objectLockTimestampPrecision)
}

func newRetainedStorageError(operation string, classification, cause error) error {
	return &RetainedStorageError{
		operation:      operation,
		classification: classification,
		cause:          cause,
	}
}

func isNilRetainedObjectAPI(value RetainedObjectAPI) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
