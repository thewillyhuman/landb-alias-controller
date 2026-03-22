package openstack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- normalizeAlias ---

func TestNormalizeAlias_RemovesLoadSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "lowercase suffix", input: "myapp--load-0-", want: "myapp"},
		{name: "uppercase suffix", input: "myapp--LOAD-1-", want: "myapp"},
		{name: "mixed case suffix", input: "myapp--Load-2-", want: "myapp"},
		{name: "high index", input: "myapp--load-99-", want: "myapp"},
		{name: "no suffix", input: "myapp", want: "myapp"},
		{name: "empty string", input: "", want: ""},
		{name: "whitespace only", input: "   ", want: ""},
		{name: "suffix in middle is kept", input: "app--load-0--extra", want: "app--load-0--extra"},
		{name: "double suffix strips trailing", input: "app--load-0---load-1-", want: "app--load-0-"},
		{name: "alias with dots", input: "my.app--load-0-", want: "my.app"},
		{name: "alias with hyphens", input: "my-app--load-0-", want: "my-app"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeAlias(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- addSuffix ---

func TestAddSuffix(t *testing.T) {
	aliases := []string{"app1", "app2"}

	got := addSuffix(aliases, 0)
	assert.Equal(t, []string{"app1--load-0-", "app2--load-0-"}, got)

	got = addSuffix(aliases, 3)
	assert.Equal(t, []string{"app1--load-3-", "app2--load-3-"}, got)
}

func TestAddSuffix_Empty(t *testing.T) {
	got := addSuffix([]string{}, 0)
	assert.Empty(t, got)
}

// --- metadataKey ---

func TestMetadataKey(t *testing.T) {
	assert.Equal(t, "landb-alias", metadataKey(0))
	assert.Equal(t, "landb-alias2", metadataKey(1))
	assert.Equal(t, "landb-alias3", metadataKey(2))
	assert.Equal(t, "landb-alias10", metadataKey(9))
}

// --- packAliases ---

func TestPackAliases_SingleKey(t *testing.T) {
	aliases := []string{"app1--load-0-", "app2--load-0-"}
	packed, err := packAliases(aliases)
	require.NoError(t, err)
	assert.Len(t, packed, 1)
	assert.Equal(t, "app1--load-0-,app2--load-0-", packed["landb-alias"])
}

func TestPackAliases_Overflow(t *testing.T) {
	// Create aliases that collectively exceed 200 chars to force overflow.
	var aliases []string
	for i := 0; i < 20; i++ {
		aliases = append(aliases, strings.Repeat("a", 15)+addSuffix([]string{""}, i)[0])
	}
	packed, err := packAliases(aliases)
	require.NoError(t, err)
	// Should have more than one key.
	assert.Greater(t, len(packed), 1, "expected overflow to multiple keys")

	// Verify all values are within the limit.
	for key, val := range packed {
		assert.LessOrEqual(t, len(val), metadataCharLimit,
			"key %s value exceeds limit: %d chars", key, len(val))
	}
}

func TestPackAliases_SingleAliasExceedsLimit(t *testing.T) {
	longAlias := strings.Repeat("x", metadataCharLimit+1)
	_, err := packAliases([]string{longAlias})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeding the")
}

func TestPackAliases_Empty(t *testing.T) {
	packed, err := packAliases([]string{})
	require.NoError(t, err)
	assert.Empty(t, packed)
}

func TestPackAliases_Sorted(t *testing.T) {
	aliases := []string{"zebra--load-0-", "alpha--load-0-", "mid--load-0-"}
	packed, err := packAliases(aliases)
	require.NoError(t, err)
	// The value should be sorted alphabetically.
	val := packed["landb-alias"]
	parts := strings.Split(val, ",")
	assert.Equal(t, []string{"alpha--load-0-", "mid--load-0-", "zebra--load-0-"}, parts)
}

// --- unpackAliases ---

func TestUnpackAliases(t *testing.T) {
	metadata := map[string]string{
		"landb-alias":  "app1--load-0-,app2--load-0-",
		"landb-alias2": "app3--load-0-",
		"unrelated":    "should-be-ignored",
	}
	got := unpackAliases(metadata)
	assert.Equal(t, []string{"app1--load-0-", "app2--load-0-", "app3--load-0-"}, got)
}

func TestUnpackAliases_EmptyValues(t *testing.T) {
	metadata := map[string]string{
		"landb-alias":  "",
		"landb-alias2": "  ",
	}
	got := unpackAliases(metadata)
	assert.Empty(t, got)
}

func TestUnpackAliases_WhitespaceHandling(t *testing.T) {
	metadata := map[string]string{
		"landb-alias": "  app1--load-0- , app2--load-0-  ",
	}
	got := unpackAliases(metadata)
	assert.Equal(t, []string{"app1--load-0-", "app2--load-0-"}, got)
}

// --- round-trip: pack then unpack ---

func TestPackUnpackRoundTrip(t *testing.T) {
	original := []string{"alpha--load-0-", "bravo--load-0-", "charlie--load-0-"}
	packed, err := packAliases(original)
	require.NoError(t, err)

	unpacked := unpackAliases(packed)
	assert.Equal(t, original, unpacked)
}
