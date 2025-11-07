package plan

import (
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ps is a helper function to easily create a PropertySet.
func ps(m map[string]string) *openstack.PropertySet {
	return openstack.NewPropertySet(m)
}

// --- TESTS ---

// TestCalculate_AddNewAlias tests the Calculate function when a new alias is added.
func TestCalculate_AddNewAlias(t *testing.T) {
	current := ps(map[string]string{
		"landb-alias": "aliasA--load-0-",
	})
	desired := ps(map[string]string{
		"landb-alias":  "aliasA--load-0-",
		"landb-alias2": "aliasB--load-0-,aliasC--load-0-",
	})

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	update := map[string]string{}
	result.Changes.Update.ForEach(func(k, v string) { update[k] = v })
	deleteSet := map[string]string{}
	result.Changes.Delete.ForEach(func(k, v string) { deleteSet[k] = v })

	assert.Equal(t, map[string]string{
		"landb-alias":  "aliasA--load-0-",
		"landb-alias2": "aliasB--load-0-,aliasC--load-0-",
	}, update)
	assert.Empty(t, deleteSet)
}

// TestCalculate_RemoveAlias tests the Calculate function when an alias is removed.
func TestCalculate_RemoveAlias(t *testing.T) {
	current := ps(map[string]string{
		"landb-alias":  "aliasA--load-0-",
		"landb-alias2": "aliasB--load-0-,aliasC--load-0-",
	})
	desired := ps(map[string]string{
		"landb-alias": "aliasA--load-0-",
	})

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	update := map[string]string{}
	result.Changes.Update.ForEach(func(k, v string) { update[k] = v })
	deleteSet := map[string]string{}
	result.Changes.Delete.ForEach(func(k, v string) { deleteSet[k] = v })

	assert.Equal(t, map[string]string{
		"landb-alias": "aliasA--load-0-",
	}, update)
	assert.Equal(t, map[string]string{
		"landb-alias2": "aliasB--load-0-,aliasC--load-0-",
	}, deleteSet)
}

// TestCalculate_ReplaceAlias tests the Calculate function when an alias is replaced.
func TestCalculate_ReplaceAlias(t *testing.T) {
	current := ps(map[string]string{
		"landb-alias": "aliasA--load-0-,aliasB--load-0-",
	})
	desired := ps(map[string]string{
		"landb-alias": "aliasA--load-1-,aliasC--load-1-",
	})

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	update := map[string]string{}
	result.Changes.Update.ForEach(func(k, v string) { update[k] = v })
	deleteSet := map[string]string{}
	result.Changes.Delete.ForEach(func(k, v string) { deleteSet[k] = v })

	assert.Equal(t, map[string]string{
		"landb-alias": "aliasA--load-1-,aliasC--load-1-",
	}, update)
	assert.Empty(t, deleteSet)
}

// TestCalculate_RemoveAll tests the Calculate function when all aliases are removed.
func TestCalculate_RemoveAll(t *testing.T) {
	current := ps(map[string]string{
		"landb-alias":  "aliasA--load-0-,aliasB--load-0-",
		"landb-alias2": "aliasC--load-0-,aliasD--load-0-",
	})
	desired := ps(map[string]string{})

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	update := map[string]string{}
	result.Changes.Update.ForEach(func(k, v string) { update[k] = v })
	deleteSet := map[string]string{}
	result.Changes.Delete.ForEach(func(k, v string) { deleteSet[k] = v })

	assert.Empty(t, update)
	assert.Equal(t, map[string]string{
		"landb-alias":  "aliasA--load-0-,aliasB--load-0-",
		"landb-alias2": "aliasC--load-0-,aliasD--load-0-",
	}, deleteSet)
}

// TestCalculate_NoChange tests the Calculate function when there are no changes.
func TestCalculate_NoChange(t *testing.T) {
	current := ps(map[string]string{
		"landb-alias":  "aliasA--load-0-,aliasB--load-0-",
		"landb-alias2": "aliasC--load-0-,aliasD--load-0-",
	})
	desired := ps(map[string]string{
		"landb-alias":  "aliasA--load-0-,aliasB--load-0-",
		"landb-alias2": "aliasC--load-0-,aliasD--load-0-",
	})

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	update := map[string]string{}
	result.Changes.Update.ForEach(func(k, v string) { update[k] = v })
	deleteSet := map[string]string{}
	result.Changes.Delete.ForEach(func(k, v string) { deleteSet[k] = v })

	assert.Equal(t, map[string]string{
		"landb-alias":  "aliasA--load-0-,aliasB--load-0-",
		"landb-alias2": "aliasC--load-0-,aliasD--load-0-",
	}, update)
	assert.Empty(t, deleteSet)
}

// TestCalculate_NilInputs tests the Calculate function when the inputs are nil.
func TestCalculate_NilInputs(t *testing.T) {
	t.Run("nil current", func(t *testing.T) {
		p := &Plan{Current: nil, Desired: ps(map[string]string{"landb-alias": "aliasA--load-0-"})}
		result := p.Calculate()
		assert.Nil(t, result.Changes)
	})

	t.Run("nil desired", func(t *testing.T) {
		p := &Plan{Current: ps(map[string]string{"landb-alias": "aliasA--load-0-"}), Desired: nil}
		result := p.Calculate()
		assert.Nil(t, result.Changes)
	})
}

// TestCalculate_DeepCopy tests that the Calculate function returns a deep copy of the plan.
func TestCalculate_DeepCopy(t *testing.T) {
	current := ps(map[string]string{"landb-alias": "aliasA--load-0-"})
	desired := ps(map[string]string{"landb-alias": "aliasA--load-0-,aliasB--load-0-"})

	p := &Plan{Current: current, Desired: desired}
	result := p.Calculate()

	// mutate originals
	current.Set("landb-alias", "MUTATED")
	desired.Set("landb-alias", "MUTATED")

	// result should remain stable
	val, _ := result.Current.Get("landb-alias")
	assert.Equal(t, "aliasA--load-0-", val)
	val, _ = result.Desired.Get("landb-alias")
	assert.Equal(t, "aliasA--load-0-,aliasB--load-0-", val)
}
