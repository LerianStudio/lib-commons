//go:build unit

// Copyright 2025 Lerian Studio.

package service

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
)

func TestNewComponentDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		defs           []domain.KeyDef
		wantComponents []string
	}{
		{
			name:           "empty defs produce no components",
			defs:           nil,
			wantComponents: []string{},
		},
		{
			name: "all ComponentNone keys are excluded",
			defs: []domain.KeyDef{
				{Key: "k1", Component: domain.ComponentNone, ApplyBehavior: domain.ApplyBundleRebuild},
				{Key: "k2", Component: domain.ComponentNone, ApplyBehavior: domain.ApplyBundleRebuildAndReconcile},
			},
			wantComponents: []string{},
		},
		{
			name: "empty component string does not become a concrete component",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "", ApplyBehavior: domain.ApplyBundleRebuild},
			},
			wantComponents: []string{},
		},
		{
			name: "bootstrap-only keys still contribute known components",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "postgres", ApplyBehavior: domain.ApplyBootstrapOnly},
			},
			wantComponents: []string{"postgres"},
		},
		{
			name: "live-read keys still contribute known components",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "redis", ApplyBehavior: domain.ApplyLiveRead},
			},
			wantComponents: []string{"redis"},
		},
		{
			name: "worker-reconcile keys still contribute known components",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "rabbitmq", ApplyBehavior: domain.ApplyWorkerReconcile},
			},
			wantComponents: []string{"rabbitmq"},
		},
		{
			name: "bundle-rebuild keys are included",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "postgres", ApplyBehavior: domain.ApplyBundleRebuild},
			},
			wantComponents: []string{"postgres"},
		},
		{
			name: "bundle-rebuild-and-reconcile keys are included",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "redis", ApplyBehavior: domain.ApplyBundleRebuildAndReconcile},
			},
			wantComponents: []string{"redis"},
		},
		{
			name: "multiple components sorted",
			defs: []domain.KeyDef{
				{Key: "k1", Component: "redis", ApplyBehavior: domain.ApplyBundleRebuild},
				{Key: "k2", Component: "postgres", ApplyBehavior: domain.ApplyBundleRebuild},
				{Key: "k3", Component: "s3", ApplyBehavior: domain.ApplyBundleRebuildAndReconcile},
			},
			wantComponents: []string{"postgres", "redis", "s3"},
		},
		{
			name: "duplicate component from multiple keys is deduplicated",
			defs: []domain.KeyDef{
				{Key: "pg.host", Component: "postgres", ApplyBehavior: domain.ApplyBundleRebuild},
				{Key: "pg.port", Component: "postgres", ApplyBehavior: domain.ApplyBundleRebuild},
			},
			wantComponents: []string{"postgres"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			diff := NewComponentDiff(tt.defs)
			assert.Equal(t, tt.wantComponents, diff.AllComponents())
		})
	}
}

func TestComponentDiff_ChangedComponents(t *testing.T) {
	t.Parallel()

	// Shared defs used by most sub-tests: two postgres keys + one redis key.
	baseDefs := []domain.KeyDef{
		{Key: "pg.host", Component: "postgres", ApplyBehavior: domain.ApplyBundleRebuild},
		{Key: "pg.port", Component: "postgres", ApplyBehavior: domain.ApplyBundleRebuild},
		{Key: "redis.url", Component: "redis", ApplyBehavior: domain.ApplyBundleRebuild},
	}

	tests := []struct {
		name    string
		defs    []domain.KeyDef
		prev    domain.Snapshot
		current domain.Snapshot
		want    map[string]bool
	}{
		{
			name: "no defs means no changed components",
			defs: nil,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "localhost"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "remote"},
			}},
			want: map[string]bool{},
		},
		{
			name: "nil prev configs triggers full rebuild",
			defs: baseDefs,
			prev: domain.Snapshot{},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "localhost"},
				"redis.url": {Value: "redis://host"},
			}},
			want: map[string]bool{"postgres": true, "redis": true},
		},
		{
			name: "empty prev configs triggers full rebuild",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "localhost"},
			}},
			want: map[string]bool{"postgres": true, "redis": true},
		},
		{
			name: "initial boot includes bootstrap-only components",
			defs: append(baseDefs, domain.KeyDef{Key: "server.address", Component: "http", ApplyBehavior: domain.ApplyBootstrapOnly}),
			prev: domain.Snapshot{},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":        {Value: "localhost"},
				"redis.url":      {Value: "redis://host"},
				"server.address": {Value: ":8080"},
			}},
			want: map[string]bool{"http": true, "postgres": true, "redis": true},
		},
		{
			name: "single component with one changed key",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "old-host"},
				"pg.port":   {Value: 5432},
				"redis.url": {Value: "redis://same"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "new-host"},
				"pg.port":   {Value: 5432},
				"redis.url": {Value: "redis://same"},
			}},
			want: map[string]bool{"postgres": true},
		},
		{
			name: "no changes returns empty set",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "same"},
				"pg.port":   {Value: 5432},
				"redis.url": {Value: "redis://same"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "same"},
				"pg.port":   {Value: 5432},
				"redis.url": {Value: "redis://same"},
			}},
			want: map[string]bool{},
		},
		{
			name: "multiple components changed in one diff",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "old-host"},
				"pg.port":   {Value: 5432},
				"redis.url": {Value: "redis://old"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "new-host"},
				"pg.port":   {Value: 5432},
				"redis.url": {Value: "redis://new"},
			}},
			want: map[string]bool{"postgres": true, "redis": true},
		},
		{
			name: "global setting change triggers component rebuild",
			defs: append(baseDefs, domain.KeyDef{Key: "fees.fail_closed_default", Component: "fees", ApplyBehavior: domain.ApplyBundleRebuild, Kind: domain.KindSetting}),
			prev: domain.Snapshot{GlobalSettings: map[string]domain.EffectiveValue{
				"fees.fail_closed_default": {Value: false},
			}},
			current: domain.Snapshot{GlobalSettings: map[string]domain.EffectiveValue{
				"fees.fail_closed_default": {Value: true},
			}},
			want: map[string]bool{"fees": true},
		},
		{
			name: "tenant setting change triggers component rebuild",
			defs: append(baseDefs, domain.KeyDef{Key: "fees.max_fee_amount_cents", Component: "fees", ApplyBehavior: domain.ApplyBundleRebuild, Kind: domain.KindSetting}),
			prev: domain.Snapshot{TenantSettings: map[string]map[string]domain.EffectiveValue{
				"tenant-a": {
					"fees.max_fee_amount_cents": {Value: 100},
				},
			}},
			current: domain.Snapshot{TenantSettings: map[string]map[string]domain.EffectiveValue{
				"tenant-a": {
					"fees.max_fee_amount_cents": {Value: 200},
				},
			}},
			want: map[string]bool{"fees": true},
		},
		{
			name: "key exists in current but not prev marks component changed",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "host"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "host"},
				"redis.url": {Value: "redis://new"},
			}},
			want: map[string]bool{"redis": true},
		},
		{
			name: "key exists in prev but not current marks component changed",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "host"},
				"redis.url": {Value: "redis://old"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "host"},
			}},
			want: map[string]bool{"redis": true},
		},
		{
			name: "both keys absent for a component means no change",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "host"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: "host"},
			}},
			want: map[string]bool{},
		},
		{
			name: "deep equality catches structural changes",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: []string{"host-a", "host-b"}},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host": {Value: []string{"host-a", "host-c"}},
			}},
			want: map[string]bool{"postgres": true},
		},
		{
			name: "unknown changed key forces full rebuild for safety",
			defs: baseDefs,
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "same"},
				"unknown.k": {Value: "old"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":   {Value: "same"},
				"unknown.k": {Value: "new"},
			}},
			want: map[string]bool{"postgres": true, "redis": true},
		},
		{
			name: "known live-read change does not rebuild components",
			defs: append(baseDefs, domain.KeyDef{Key: "app.log_level", Component: domain.ComponentNone, ApplyBehavior: domain.ApplyLiveRead}),
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":       {Value: "same"},
				"app.log_level": {Value: "info"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":       {Value: "same"},
				"app.log_level": {Value: "debug"},
			}},
			want: map[string]bool{},
		},
		{
			name: "known bootstrap-only change does not rebuild components after boot",
			defs: append(baseDefs, domain.KeyDef{Key: "server.address", Component: "http", ApplyBehavior: domain.ApplyBootstrapOnly}),
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":        {Value: "same"},
				"server.address": {Value: ":8080"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":        {Value: "same"},
				"server.address": {Value: ":8081"},
			}},
			want: map[string]bool{},
		},
		{
			name: "known worker-reconcile change does not rebuild components",
			defs: append(baseDefs, domain.KeyDef{Key: "worker.interval", Component: "worker", ApplyBehavior: domain.ApplyWorkerReconcile}),
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":         {Value: "same"},
				"worker.interval": {Value: 1},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":         {Value: "same"},
				"worker.interval": {Value: 2},
			}},
			want: map[string]bool{},
		},
		{
			name: "rebuild key with ComponentNone stays excluded",
			defs: append(baseDefs, domain.KeyDef{Key: "feature.toggle", Component: domain.ComponentNone, ApplyBehavior: domain.ApplyBundleRebuild}),
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"feature.toggle": {Value: false},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"feature.toggle": {Value: true},
			}},
			want: map[string]bool{},
		},
		{
			name: "unclassified rebuild key forces full rebuild",
			defs: append(baseDefs, domain.KeyDef{Key: "pg.unclassified", Component: "", ApplyBehavior: domain.ApplyBundleRebuild}),
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":         {Value: "same"},
				"pg.unclassified": {Value: "old"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":         {Value: "same"},
				"pg.unclassified": {Value: "new"},
			}},
			want: map[string]bool{"postgres": true, "redis": true},
		},
		{
			name: "invalid apply behavior forces full rebuild for safety",
			defs: append(baseDefs, domain.KeyDef{Key: "pg.invalid", Component: "postgres", ApplyBehavior: domain.ApplyBehavior("invalid")}),
			prev: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":    {Value: "same"},
				"pg.invalid": {Value: "old"},
			}},
			current: domain.Snapshot{Configs: map[string]domain.EffectiveValue{
				"pg.host":    {Value: "same"},
				"pg.invalid": {Value: "new"},
			}},
			want: map[string]bool{"postgres": true, "redis": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			diff := NewComponentDiff(tt.defs)
			got := diff.ChangedComponents(tt.prev, tt.current)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestComponentDiff_NilReceiver(t *testing.T) {
	t.Parallel()

	var diff *ComponentDiff

	assert.Equal(t, map[string]bool{}, diff.ChangedComponents(domain.Snapshot{}, domain.Snapshot{}))
	assert.Equal(t, []string{}, diff.AllComponents())
}

func TestComponentDiff_AllComponents_Sorted(t *testing.T) {
	t.Parallel()

	defs := []domain.KeyDef{
		{Key: "z.key", Component: "zebra", ApplyBehavior: domain.ApplyBundleRebuild},
		{Key: "a.key", Component: "alpha", ApplyBehavior: domain.ApplyBundleRebuild},
		{Key: "m.key", Component: "middle", ApplyBehavior: domain.ApplyBundleRebuildAndReconcile},
	}

	diff := NewComponentDiff(defs)

	assert.Equal(t, []string{"alpha", "middle", "zebra"}, diff.AllComponents())
}
