package plan

import (
	"testing"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/dns"

	"github.com/stretchr/testify/assert"
)

const (
	defaultTTL = 300
)

// rec is a helper function to easily create a simple DNS 'A' record.
func rec(name, ip string) *dns.Record {
	return &dns.Record{
		Name:   name,
		Type:   "A",
		TTL:    defaultTTL,
		Values: []string{ip},
	}
}

// recWithTTL is a helper to create a record with a specific TTL.
func recWithTTL(name, ip string, ttl int) *dns.Record {
	r := rec(name, ip)
	r.TTL = ttl
	return r
}

// recWithValues is a helper to create a record with multiple values.
func recWithValues(name string, values []string) *dns.Record {
	r := rec(name, "0.0.0.0") // ip is irrelevant
	r.Values = values
	return r
}

// --- Test Helpers to Assert Changes ---

// getCreates returns a map[key]*dns.Record for easy assertion.
func getCreates(c *Changes) map[string]*dns.Record {
	m := make(map[string]*dns.Record)
	for _, r := range c.Create {
		m[recordKey(r)] = r
	}
	return m
}

// getDeletes returns a map[key]*dns.Record for easy assertion.
func getDeletes(c *Changes) map[string]*dns.Record {
	m := make(map[string]*dns.Record)
	for _, r := range c.Delete {
		m[recordKey(r)] = r
	}
	return m
}

// getUpdates returns a map[key]*UpdateChange for easy assertion.
func getUpdates(c *Changes) map[string]*UpdateChange {
	m := make(map[string]*UpdateChange)
	for _, u := range c.Update {
		m[recordKey(u.Desired)] = u
	}
	return m
}

// --- TESTS ---

// TestCalculate_Create tests adding a new record.
func TestCalculate_Create(t *testing.T) {
	current := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
	}
	desired := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
		rec("aliasB.com", "2.2.2.2"), // New record
	}

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	creates := getCreates(result.Changes)
	updates := getUpdates(result.Changes)
	deletes := getDeletes(result.Changes)

	assert.Len(t, creates, 1, "Should be one create")
	assert.Contains(t, creates, "aliasb.com:A")
	assert.Equal(t, "aliasB.com", creates["aliasb.com:A"].Name)
	assert.Equal(t, []string{"2.2.2.2"}, creates["aliasb.com:A"].Values)

	assert.Empty(t, updates, "Should be no updates")
	assert.Empty(t, deletes, "Should be no deletes")
}

// TestCalculate_Delete tests removing an existing record.
func TestCalculate_Delete(t *testing.T) {
	current := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
		rec("aliasB.com", "2.2.2.2"), // To be deleted
	}
	desired := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
	}

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	creates := getCreates(result.Changes)
	updates := getUpdates(result.Changes)
	deletes := getDeletes(result.Changes)

	assert.Empty(t, creates, "Should be no creates")
	assert.Empty(t, updates, "Should be no updates")

	assert.Len(t, deletes, 1, "Should be one delete")
	assert.Contains(t, deletes, "aliasb.com:A")
	assert.Equal(t, "aliasB.com", deletes["aliasb.com:A"].Name)
}

// TestCalculate_Update tests modifying an existing record.
func TestCalculate_Update(t *testing.T) {
	t.Run("Update IP Value", func(t *testing.T) {
		current := []*dns.Record{
			rec("aliasA.com", "1.1.1.1"), // Old IP
		}
		desired := []*dns.Record{
			rec("aliasA.com", "2.2.2.2"), // New IP
		}

		p := &Plan{Current: current, Desired: desired}
		result := p.Calculate()

		creates := getCreates(result.Changes)
		updates := getUpdates(result.Changes)
		deletes := getDeletes(result.Changes)

		assert.Empty(t, creates)
		assert.Empty(t, deletes)
		assert.Len(t, updates, 1)
		assert.Contains(t, updates, "aliasa.com:A")

		update := updates["aliasa.com:A"]
		assert.Equal(t, []string{"1.1.1.1"}, update.Current.Values)
		assert.Equal(t, []string{"2.2.2.2"}, update.Desired.Values)
	})

	t.Run("Update TTL", func(t *testing.T) {
		current := []*dns.Record{
			recWithTTL("aliasA.com", "1.1.1.1", 300), // Old TTL
		}
		desired := []*dns.Record{
			recWithTTL("aliasA.com", "1.1.1.1", 600), // New TTL
		}

		p := &Plan{Current: current, Desired: desired}
		result := p.Calculate()
		updates := getUpdates(result.Changes)

		assert.Empty(t, getCreates(result.Changes))
		assert.Empty(t, getDeletes(result.Changes))
		assert.Len(t, updates, 1)
		assert.Contains(t, updates, "aliasa.com:A")
		assert.Equal(t, 300, updates["aliasa.com:A"].Current.TTL)
		assert.Equal(t, 600, updates["aliasa.com:A"].Desired.TTL)
	})
}

// TestCalculate_Replace is a combination of Delete and Create.
func TestCalculate_Replace(t *testing.T) {
	current := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"), // To be deleted
	}
	desired := []*dns.Record{
		rec("aliasB.com", "2.2.2.2"), // To be created
	}

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	creates := getCreates(result.Changes)
	updates := getUpdates(result.Changes)
	deletes := getDeletes(result.Changes)

	assert.Len(t, creates, 1, "Should be one create")
	assert.Contains(t, creates, "aliasb.com:A")

	assert.Empty(t, updates, "Should be no updates")

	assert.Len(t, deletes, 1, "Should be one delete")
	assert.Contains(t, deletes, "aliasa.com:A")
}

// TestCalculate_RemoveAll tests removing all records.
func TestCalculate_RemoveAll(t *testing.T) {
	current := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
		rec("aliasB.com", "2.2.2.2"),
	}
	desired := []*dns.Record{}

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	creates := getCreates(result.Changes)
	updates := getUpdates(result.Changes)
	deletes := getDeletes(result.Changes)

	assert.Empty(t, creates)
	assert.Empty(t, updates)

	assert.Len(t, deletes, 2)
	assert.Contains(t, deletes, "aliasa.com:A")
	assert.Contains(t, deletes, "aliasb.com:A")
}

// TestCalculate_NoChange tests when current and desired are identical.
func TestCalculate_NoChange(t *testing.T) {
	current := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
		rec("aliasB.com", "2.2.2.2"),
	}
	desired := []*dns.Record{
		rec("aliasA.com", "1.1.1.1"),
		rec("aliasB.com", "2.2.2.2"),
	}

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	creates := getCreates(result.Changes)
	updates := getUpdates(result.Changes)
	deletes := getDeletes(result.Changes)

	assert.Empty(t, creates, "Should be no creates")
	assert.Empty(t, updates, "Should be no updates")
	assert.Empty(t, deletes, "Should be no deletes")
}

// TestCalculate_ComplexMix tests all operations at once.
func TestCalculate_ComplexMix(t *testing.T) {
	current := []*dns.Record{
		rec("no-op.com", "1.1.1.1"),     // No change
		rec("update-me.com", "1.1.1.1"), // To be updated
		rec("delete-me.com", "3.3.3.3"), // To be deleted
	}
	desired := []*dns.Record{
		rec("no-op.com", "1.1.1.1"),     // No change
		rec("update-me.com", "2.2.2.2"), // Updated
		rec("create-me.com", "4.4.4.4"), // To be created
	}

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	creates := getCreates(result.Changes)
	updates := getUpdates(result.Changes)
	deletes := getDeletes(result.Changes)

	// Check Create
	assert.Len(t, creates, 1)
	assert.Contains(t, creates, "create-me.com:A")
	assert.Equal(t, "4.4.4.4", creates["create-me.com:A"].Values[0])

	// Check Update
	assert.Len(t, updates, 1)
	assert.Contains(t, updates, "update-me.com:A")
	assert.Equal(t, "1.1.1.1", updates["update-me.com:A"].Current.Values[0])
	assert.Equal(t, "2.2.2.2", updates["update-me.com:A"].Desired.Values[0])

	// Check Delete
	assert.Len(t, deletes, 1)
	assert.Contains(t, deletes, "delete-me.com:A")
	assert.Equal(t, "3.3.3.3", deletes["delete-me.com:A"].Values[0])
}

// TestCalculate_NilInputs tests handling of nil slices.
func TestCalculate_NilInputs(t *testing.T) {
	t.Run("nil current", func(t *testing.T) {
		p := &Plan{Current: nil, Desired: []*dns.Record{rec("aliasA.com", "1.1.1.1")}}
		result := p.Calculate()
		assert.Nil(t, result.Changes, "Changes should be nil on nil input")
	})

	t.Run("nil desired", func(t *testing.T) {
		p := &Plan{Current: []*dns.Record{rec("aliasA.com", "1.1.1.1")}, Desired: nil}
		result := p.Calculate()
		assert.Nil(t, result.Changes, "Changes should be nil on nil input")
	})
}

// TestRecordsAreEqual tests the internal helper function.
func TestRecordsAreEqual(t *testing.T) {
	base := rec("test.com", "1.1.1.1")

	t.Run("Identical", func(t *testing.T) {
		other := rec("test.com", "1.1.1.1")
		assert.True(t, recordsAreEqual(base, other))
	})

	t.Run("Different TTL", func(t *testing.T) {
		other := recWithTTL("test.com", "1.1.1.1", 600)
		assert.False(t, recordsAreEqual(base, other))
	})

	t.Run("Different Value", func(t *testing.T) {
		other := rec("test.com", "2.2.2.2")
		assert.False(t, recordsAreEqual(base, other))
	})

	t.Run("Different Value Count", func(t *testing.T) {
		other := recWithValues("test.com", []string{"1.1.1.1", "2.2.2.2"})
		assert.False(t, recordsAreEqual(base, other))
	})

	t.Run("Same Values, Different Order", func(t *testing.T) {
		r1 := recWithValues("test.com", []string{"1.1.1.1", "2.2.2.2"})
		r2 := recWithValues("test.com", []string{"2.2.2.2", "1.1.1.1"})
		assert.True(t, recordsAreEqual(r1, r2), "Should be equal regardless of value order")
	})

	t.Run("Different Values, Same Order", func(t *testing.T) {
		r1 := recWithValues("test.com", []string{"1.all", "2.2.2.2"})
		r2 := recWithValues("test.com", []string{"1.1.1.1", "2.2.2.2"})
		assert.False(t, recordsAreEqual(r1, r2))
	})
}
