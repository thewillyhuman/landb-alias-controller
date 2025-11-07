package plan

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"
	"sort"
	"strings"
)

// Plan represents the reconciliation difference between the current
// and desired OpenStack metadata (PropertySets). It determines which
// metadata entries should be updated or deleted to reach the desired state.
type Plan struct {
	// Current is the current metadata retrieved from OpenStack.
	Current *openstack.PropertySet // Current metadata retrieved from OpenStack
	// Desired is the desired metadata computed by the controller.
	Desired *openstack.PropertySet // Desired metadata computed by the controller
	// Changes is the computed differences (updates and deletions).
	Changes *Changes // Computed differences (updates and deletions)
}

// Changes contains the sets of key-value pairs to update or delete
// when applying a reconciliation plan.
type Changes struct {
	// Update is the set of keys/values to add or modify.
	Update *openstack.PropertySet // Keys/values to add or modify
	// Delete is the set of keys/values to remove.
	Delete *openstack.PropertySet // Keys/values to remove
}

// Calculate computes the delta between the current and desired state.
// - Any key present in Desired (regardless of Current) goes to Updates.
// - Any key present in Current but missing from Desired goes to Deletes.
//
// It returns a new Plan object with the computed Changes section populated.
// The input Plan is not modified.
//
// Logs are emitted at DEBUG level with summary information.
func (p *Plan) Calculate() *Plan {
	if p.Current == nil || p.Desired == nil {
		log.Errorf("plan calculation failed: both Current and Desired must be defined (got Current=%v, Desired=%v)", p.Current, p.Desired)
		return p
	}

	log.Debugf("Calculating reconciliation plan. Current=%v Desired=%v", *p.Current, *p.Desired)

	// Prepare maps to store computed updates/deletions
	updateSet := *p.Desired.Clone()
	deleteSet := openstack.NewPropertySet(map[string]string{})

	// Any key in current but not in desired → mark for deletion
	for key, val := range p.Current.AsMap() {
		if _, exists := p.Desired.Get(key); !exists {
			deleteSet.Set(key, val)
		}
	}

	// Deep copy to avoid modification of the original plan.
	currentC := *p.Current.Clone()
	desiredC := *p.Desired.Clone()
	changes := &Changes{Update: &updateSet, Delete: deleteSet}
	plan := &Plan{Current: &currentC, Desired: &desiredC, Changes: changes}

	log.Debugf("Plan calculated. Update=%v Delete=%v", plan.Changes.Update, plan.Changes.Delete)
	return plan
}

// ToString returns a single-line string representation of the Plan,
// including Current, Desired, and Changes (Update/Delete).
func (p *Plan) ToString() string {
	var sb strings.Builder

	sb.WriteString("Plan{")

	sb.WriteString("Current={")
	if p.Current != nil {
		sb.WriteString(propertySetToString(p.Current))
	}
	sb.WriteString("}, ")

	sb.WriteString("Desired={")
	if p.Desired != nil {
		sb.WriteString(propertySetToString(p.Desired))
	}
	sb.WriteString("}, ")

	sb.WriteString("Changes={")
	if p.Changes != nil {
		sb.WriteString("Update={")
		if p.Changes.Update != nil {
			sb.WriteString(propertySetToString(p.Changes.Update))
		}
		sb.WriteString("}, Delete={")
		if p.Changes.Delete != nil {
			sb.WriteString(propertySetToString(p.Changes.Delete))
		}
		sb.WriteString("}")
	}
	sb.WriteString("}")

	sb.WriteString("}")

	return sb.String()
}

// propertySetToString returns a deterministic string representation of a PropertySet.
func propertySetToString(ps *openstack.PropertySet) string {
	if ps == nil {
		return ""
	}

	m := ps.AsMap()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(m))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
	}

	return strings.Join(parts, ", ")
}
