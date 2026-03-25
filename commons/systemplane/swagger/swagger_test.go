// Copyright 2025 Lerian Studio.

package swagger

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpec_ReturnsValidJSON(t *testing.T) {
	raw := Spec()
	require.NotEmpty(t, raw, "Spec() must return non-empty data")

	var doc map[string]json.RawMessage
	err := json.Unmarshal(raw, &doc)
	require.NoError(t, err, "Spec() must return valid JSON")
	assert.Contains(t, string(doc["swagger"]), "2.0", "spec must be OpenAPI 2.0")
}

func TestSpec_ContainsAllPaths(t *testing.T) {
	raw := Spec()

	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}

	require.NoError(t, json.Unmarshal(raw, &doc))

	expectedPaths := []string{
		"/v1/system/configs",
		"/v1/system/configs/schema",
		"/v1/system/configs/history",
		"/v1/system/configs/reload",
		"/v1/system/settings",
		"/v1/system/settings/schema",
		"/v1/system/settings/history",
	}

	for _, path := range expectedPaths {
		assert.Contains(t, doc.Paths, path, "missing path: %s", path)
	}

	// Verify the combined endpoint count covers all 9 operations:
	// /configs has GET + PATCH (2), /configs/schema GET (1), /configs/history GET (1),
	// /configs/reload POST (1), /settings has GET + PATCH (2),
	// /settings/schema GET (1), /settings/history GET (1) = 9 total operations.
	totalOps := 0

	for _, pathData := range doc.Paths {
		var methods map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(pathData, &methods))

		totalOps += len(methods)
	}

	assert.Equal(t, 9, totalOps, "spec must contain exactly 9 operations")
}

func TestSpec_ContainsAllDefinitions(t *testing.T) {
	raw := Spec()

	var doc struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}

	require.NoError(t, json.Unmarshal(raw, &doc))

	expectedDefinitions := []string{
		"systemplane.ConfigsResponse",
		"systemplane.EffectiveValueDTO",
		"systemplane.PatchConfigsRequest",
		"systemplane.PatchResponse",
		"systemplane.SchemaResponse",
		"systemplane.SchemaEntryDTO",
		"systemplane.HistoryResponse",
		"systemplane.HistoryEntryDTO",
		"systemplane.SettingsResponse",
		"systemplane.PatchSettingsRequest",
		"systemplane.ErrorResponse",
		"systemplane.ReloadResponse",
	}

	for _, def := range expectedDefinitions {
		assert.Contains(t, doc.Definitions, def, "missing definition: %s", def)
	}

	assert.Len(t, doc.Definitions, len(expectedDefinitions),
		"spec must contain exactly %d definitions", len(expectedDefinitions))
}

func TestSpec_SchemaEntryDTOContainsMetadataFields(t *testing.T) {
	raw := Spec()

	var doc struct {
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}

	require.NoError(t, json.Unmarshal(raw, &doc))

	schemaEntry, ok := doc.Definitions["systemplane.SchemaEntryDTO"]
	require.True(t, ok)
	assert.Contains(t, schemaEntry.Properties, "envVar")
	assert.Contains(t, schemaEntry.Properties, "redactPolicy")
}

func TestMergeInto_EmptyTarget(t *testing.T) {
	target := []byte(`{"swagger":"2.0","info":{"title":"Test","version":"1.0"},"paths":{}}`)

	result, err := MergeInto(target)
	require.NoError(t, err)

	var doc struct {
		Swagger string                     `json:"swagger"`
		Info    map[string]string          `json:"info"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}

	require.NoError(t, json.Unmarshal(result, &doc))

	// Target metadata is preserved.
	assert.Equal(t, "2.0", doc.Swagger)
	assert.Equal(t, "Test", doc.Info["title"])

	// Systemplane paths are merged in.
	assert.Contains(t, doc.Paths, "/v1/system/configs")
	assert.Contains(t, doc.Paths, "/v1/system/settings")
}

func TestMergeInto_PreservesExistingPaths(t *testing.T) {
	target := []byte(`{
		"swagger": "2.0",
		"info": {"title": "App", "version": "1.0"},
		"paths": {
			"/v1/health": {
				"get": {"summary": "Health check"}
			}
		}
	}`)

	result, err := MergeInto(target)
	require.NoError(t, err)

	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}

	require.NoError(t, json.Unmarshal(result, &doc))

	// Original path is preserved.
	assert.Contains(t, doc.Paths, "/v1/health", "existing path must survive merge")

	// Systemplane paths are also present.
	assert.Contains(t, doc.Paths, "/v1/system/configs")
	assert.Contains(t, doc.Paths, "/v1/system/configs/reload")
}

func TestMergeInto_PreservesExistingDefinitions(t *testing.T) {
	target := []byte(`{
		"swagger": "2.0",
		"info": {"title": "App", "version": "1.0"},
		"paths": {},
		"definitions": {
			"app.MyModel": {
				"type": "object",
				"properties": {
					"id": {"type": "string"}
				}
			}
		}
	}`)

	result, err := MergeInto(target)
	require.NoError(t, err)

	var doc struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}

	require.NoError(t, json.Unmarshal(result, &doc))

	// Original definition is preserved.
	assert.Contains(t, doc.Definitions, "app.MyModel", "existing definition must survive merge")

	// Systemplane definitions are also present.
	assert.Contains(t, doc.Definitions, "systemplane.ConfigsResponse")
	assert.Contains(t, doc.Definitions, "systemplane.ErrorResponse")
}

func TestMergeInto_InvalidJSON(t *testing.T) {
	_, err := MergeInto([]byte(`not valid json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "swagger merge")
}

func TestMergeInto_MergesTags(t *testing.T) {
	target := []byte(`{
		"swagger": "2.0",
		"info": {"title": "App", "version": "1.0"},
		"paths": {},
		"tags": [
			{"name": "Health", "description": "Health check endpoints"}
		]
	}`)

	result, err := MergeInto(target)
	require.NoError(t, err)

	var doc struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}

	require.NoError(t, json.Unmarshal(result, &doc))

	tagNames := make([]string, len(doc.Tags))
	for i, tag := range doc.Tags {
		tagNames[i] = tag.Name
	}

	assert.Contains(t, tagNames, "Health", "existing tag must survive merge")
	assert.Contains(t, tagNames, "System Configs", "systemplane config tag must be merged")
	assert.Contains(t, tagNames, "System Settings", "systemplane settings tag must be merged")
	assert.Len(t, doc.Tags, 3, "must have exactly 3 tags after merge")
}
