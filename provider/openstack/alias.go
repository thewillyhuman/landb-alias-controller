// Package openstack implements the provider.Provider interface for CERN's
// OpenStack-based LANDB alias system.
//
// DNS aliases are stored as server metadata properties. A separate CERN
// system reads these properties and updates the actual DNS records.
package openstack

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	// landbAliasPrefix is the metadata key prefix used by CERN's LANDB
	// system. Keys are "landb-alias", "landb-alias2", "landb-alias3", etc.
	landbAliasPrefix = "landb-alias"

	// landbSetMetadataKey is the OpenStack metadata key used to declare the
	// landb set for a server.
	landbSetMetadataKey = "landb-set"

	// metadataCharLimit is the maximum length of a single metadata value.
	// OpenStack allows 255 characters, but we use 200 for safety margin.
	metadataCharLimit = 200
)

// loadSuffixRegex matches the LANDB load-balancer suffix appended to
// aliases for A-record creation. The format is "--load-N-" where N is
// a non-negative integer (e.g., "--load-0-", "--load-12-").
//
// See spec.md lines 27, 43-46 for the canonical suffix format.
var loadSuffixRegex = regexp.MustCompile(`(?i)--load-\d+-$`)

// normalizeAlias strips the load-balancer suffix (--load-N-) from an
// alias, returning the base alias name. If the alias has no suffix, it
// is returned unchanged. An empty input returns an empty string.
func normalizeAlias(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ""
	}
	return loadSuffixRegex.ReplaceAllString(alias, "")
}

// addSuffix appends the load-balancer suffix "--load-{nodeIndex}-" to
// each alias. The suffix causes CERN's LANDB system to create A records
// instead of CNAMEs, enabling DNS round-robin across multiple nodes.
func addSuffix(aliases []string, nodeIndex int) []string {
	suffix := fmt.Sprintf("--load-%d-", nodeIndex)
	result := make([]string, len(aliases))
	for i, alias := range aliases {
		result[i] = alias + suffix
	}
	return result
}

// packAliases distributes a list of suffixed aliases across one or more
// metadata keys, respecting the per-key character limit.
//
// The returned map uses keys "landb-alias", "landb-alias2", etc.
// Aliases within each key are comma-separated and sorted for stable output.
//
// Returns an error if any single alias exceeds metadataCharLimit, since
// it cannot fit in any key.
func packAliases(aliases []string) (map[string]string, error) {
	if len(aliases) == 0 {
		return map[string]string{}, nil
	}

	// Sort for deterministic, diffable output.
	sorted := make([]string, len(aliases))
	copy(sorted, aliases)
	sort.Strings(sorted)

	// Validate that no single alias exceeds the limit.
	for _, alias := range sorted {
		if len(alias) > metadataCharLimit {
			return nil, fmt.Errorf("alias %q is %d characters, exceeding the %d character metadata limit",
				alias, len(alias), metadataCharLimit)
		}
	}

	packed := make(map[string]string)
	var buf strings.Builder
	keyIndex := 0

	for _, alias := range sorted {
		commaLen := 0
		if buf.Len() > 0 {
			commaLen = 1
		}

		// If adding this alias would overflow, flush the current buffer.
		if buf.Len()+len(alias)+commaLen > metadataCharLimit {
			packed[metadataKey(keyIndex)] = buf.String()
			buf.Reset()
			keyIndex++
		}

		if buf.Len() > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(alias)
	}

	// Flush the remaining buffer.
	if buf.Len() > 0 {
		packed[metadataKey(keyIndex)] = buf.String()
	}

	return packed, nil
}

// unpackAliases extracts individual alias strings from a server's metadata
// map. Only keys starting with "landb-alias" are inspected. The returned
// aliases retain their suffixes (not normalized).
func unpackAliases(metadata map[string]string) []string {
	var aliases []string
	for key, value := range metadata {
		if !strings.HasPrefix(key, landbAliasPrefix) || strings.TrimSpace(value) == "" {
			continue
		}
		for _, alias := range strings.Split(value, ",") {
			alias = strings.TrimSpace(alias)
			if alias != "" {
				aliases = append(aliases, alias)
			}
		}
	}
	sort.Strings(aliases)
	return aliases
}

// metadataKey returns the LANDB metadata key name for the given index.
// Index 0 returns "landb-alias", index 1 returns "landb-alias2", etc.
func metadataKey(index int) string {
	if index == 0 {
		return landbAliasPrefix
	}
	return fmt.Sprintf("%s%d", landbAliasPrefix, index+1)
}
