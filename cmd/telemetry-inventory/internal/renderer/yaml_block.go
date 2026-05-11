package renderer

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

func writeYAMLBlock(sb *strings.Builder, indent int, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	prefix := strings.Repeat(" ", indent)
	for _, k := range keys {
		sb.WriteString(prefix)
		sb.WriteString(k)
		sb.WriteString(": ")
		writeYAMLValue(sb, indent+2, m[k])
		sb.WriteByte('\n')
	}
}

func writeYAMLValue(sb *strings.Builder, indent int, v any) {
	switch x := v.(type) {
	case string:
		sb.WriteString(strconv.Quote(x))
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case int:
		sb.WriteString(strconv.Itoa(x))
	case float64:
		sb.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case []float64:
		values := append([]float64(nil), x...)
		sort.Float64s(values)
		sb.WriteByte('[')

		for i, value := range values {
			if i > 0 {
				sb.WriteString(", ")
			}

			sb.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
		}

		sb.WriteByte(']')
	case []string:
		writeStringSlice(sb, x)
	case []schema.EmissionSite:
		writeSites(sb, indent, x)
	case map[string]int:
		writeIntMap(sb, indent, x)
	case schema.EmissionSite:
		writeYAMLValue(sb, indent, []schema.EmissionSite{x})
	default:
		// The renderer's callers (markdown.go) only pass values whose types
		// match one of the cases above — all sourced from the typed
		// schema.Dictionary fields. The "null" fallback is a structural
		// safety net for an unreachable branch: a future schema field that
		// introduces a new value type will surface here as a literal "null"
		// in the generated dictionary, which the golden tests catch
		// immediately as drift. Promoting this to an error return would
		// cascade error-handling through Render and its callers (inventory,
		// verify) for a branch that is statically unreachable today.
		sb.WriteString("null")
	}
}

func writeStringSlice(sb *strings.Builder, values []string) {
	sorted := append([]string(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	sb.WriteByte('[')

	for i, value := range sorted {
		if i > 0 {
			sb.WriteString(", ")
		}

		sb.WriteString(strconv.Quote(value))
	}

	sb.WriteByte(']')
}

func writeSites(sb *strings.Builder, indent int, values []schema.EmissionSite) {
	sites := append([]schema.EmissionSite(nil), values...)
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}

		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}

		return sites[i].Tier < sites[j].Tier
	})

	if len(sites) == 0 {
		sb.WriteString("[]")
		return
	}

	sb.WriteByte('\n')

	for i, site := range sites {
		prefix := strings.Repeat(" ", indent)
		sb.WriteString(prefix)
		sb.WriteString("- file: ")
		sb.WriteString(strconv.Quote(filepath.ToSlash(site.File)))
		sb.WriteByte('\n')
		sb.WriteString(prefix)
		sb.WriteString("  line: ")
		sb.WriteString(strconv.Itoa(site.Line))

		if site.Tier > 0 {
			sb.WriteByte('\n')
			sb.WriteString(prefix)
			sb.WriteString("  tier: ")
			sb.WriteString(strconv.Itoa(site.Tier))
		}

		// Caller's writeYAMLBlock loop appends its own newline after the
		// value; emitting one here on the final entry produces a blank
		// line inside the YAML block.
		if i < len(sites)-1 {
			sb.WriteByte('\n')
		}
	}
}

func writeIntMap(sb *strings.Builder, indent int, values map[string]int) {
	if len(values) == 0 {
		sb.WriteString("{}")
		return
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}

	sort.Strings(keys)
	sb.WriteByte('\n')

	for i, key := range keys {
		sb.WriteString(strings.Repeat(" ", indent))
		sb.WriteString(key)
		sb.WriteString(": ")
		sb.WriteString(strconv.Itoa(values[key]))

		// Same trailing-newline reasoning as writeSites: the caller's
		// writeYAMLBlock loop appends its own newline after the value.
		if i < len(keys)-1 {
			sb.WriteByte('\n')
		}
	}
}
