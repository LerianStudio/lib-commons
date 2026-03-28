// Copyright 2025 Lerian Studio.

// Package swagger provides the embedded OpenAPI 2.0 specification for systemplane
// routes. Since the systemplane handler is auto-mounted from an external Go module,
// swag init at the consuming application level cannot discover its annotations.
// This package provides the spec as an embeddable resource with a merge helper.
package swagger

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
)

//go:embed spec.json
var specBytes []byte

// Spec returns the raw embedded OpenAPI 2.0 specification for systemplane
// routes. The returned json.RawMessage is a direct reference to the embedded
// data and must not be mutated by the caller.
func Spec() json.RawMessage {
	return json.RawMessage(specBytes)
}

// MergeInto merges systemplane paths, definitions, and tags into an existing
// OpenAPI 2.0 specification. The target must be a valid JSON object
// representing a Swagger 2.0 document.
//
// Merge semantics:
//   - paths: systemplane paths are added; on key conflict systemplane wins.
//   - definitions: systemplane definitions are added; on key conflict systemplane wins.
//   - tags: systemplane tags are merged with deduplication by tag name. Tags
//     from the source with a name already present in the destination are
//     skipped. Duplicate names within the source are also collapsed.
//
// The function does not modify the target slice; it returns a new byte slice.
func MergeInto(target []byte) ([]byte, error) {
	var dst map[string]json.RawMessage
	if err := json.Unmarshal(target, &dst); err != nil {
		return nil, fmt.Errorf("swagger merge: unmarshal target: %w", err)
	}

	var src map[string]json.RawMessage
	if err := json.Unmarshal(specBytes, &src); err != nil {
		return nil, fmt.Errorf("swagger merge: unmarshal systemplane spec: %w", err)
	}

	// Merge "paths" — flat object merge, systemplane wins on conflict.
	if err := mergeObjectField("paths", dst, src); err != nil {
		return nil, err
	}

	// Merge "definitions" — flat object merge, systemplane wins on conflict.
	if err := mergeObjectField("definitions", dst, src); err != nil {
		return nil, err
	}

	// Merge "tags" — append systemplane tags to existing.
	if err := mergeTags(dst, src); err != nil {
		return nil, err
	}

	result, err := json.Marshal(dst)
	if err != nil {
		return nil, fmt.Errorf("swagger merge: marshal result: %w", err)
	}

	return result, nil
}

// mergeObjectField merges a top-level JSON object field from src into dst.
// Both dst[field] and src[field] are expected to be JSON objects whose keys
// map to arbitrary JSON values. Source keys overwrite destination keys on
// conflict. If the field is absent from src, this is a no-op.
func mergeObjectField(field string, dst, src map[string]json.RawMessage) error {
	srcField, ok := src[field]
	if !ok {
		return nil
	}

	var srcMap map[string]json.RawMessage
	if err := json.Unmarshal(srcField, &srcMap); err != nil {
		return fmt.Errorf("swagger merge: unmarshal src %s: %w", field, err)
	}

	dstMap := make(map[string]json.RawMessage)

	if dstField, ok := dst[field]; ok {
		if err := json.Unmarshal(dstField, &dstMap); err != nil {
			return fmt.Errorf("swagger merge: unmarshal dst %s: %w", field, err)
		}
	}

	maps.Copy(dstMap, srcMap)

	merged, err := json.Marshal(dstMap)
	if err != nil {
		return fmt.Errorf("swagger merge: marshal merged %s: %w", field, err)
	}

	dst[field] = json.RawMessage(merged)

	return nil
}

// mergeTags merges systemplane tags into the target's existing tags array with
// deduplication by tag name. Tags from the source whose name already exists in
// the destination are skipped; duplicate names within the source are also
// collapsed. Tag order from the destination is preserved; new source tags are
// appended in their original order.
func mergeTags(dst, src map[string]json.RawMessage) error {
	srcTags, ok := src["tags"]
	if !ok {
		return nil
	}

	var srcArr []json.RawMessage
	if err := json.Unmarshal(srcTags, &srcArr); err != nil {
		return fmt.Errorf("swagger merge: unmarshal src tags: %w", err)
	}

	dstArr := make([]json.RawMessage, 0, len(srcArr))

	if dstTags, ok := dst["tags"]; ok {
		if err := json.Unmarshal(dstTags, &dstArr); err != nil {
			return fmt.Errorf("swagger merge: unmarshal dst tags: %w", err)
		}
	}

	// Build a set of existing tag names for deduplication.
	existing := make(map[string]bool, len(dstArr))
	for _, raw := range dstArr {
		var tag struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &tag) == nil && tag.Name != "" {
			existing[tag.Name] = true
		}
	}

	// Only append tags whose name is not already present. Also deduplicate
	// within srcArr itself by marking each appended name.
	for _, raw := range srcArr {
		var tag struct {
			Name string `json:"name"`
		}

		if json.Unmarshal(raw, &tag) != nil {
			continue
		}

		if existing[tag.Name] {
			continue
		}

		existing[tag.Name] = true

		dstArr = append(dstArr, raw)
	}

	merged, err := json.Marshal(dstArr)
	if err != nil {
		return fmt.Errorf("swagger merge: marshal merged tags: %w", err)
	}

	dst["tags"] = json.RawMessage(merged)

	return nil
}
