package plan

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/dns"
)

// UpdateChange represents a required modification to an existing DNS record.
// It holds the state before and after the change.
type UpdateChange struct {
	Current *dns.Record
	Desired *dns.Record
}

// Changes contains the sets of records to create, update, or delete
// to reconcile the desired state with the current state.
type Changes struct {
	Create []*dns.Record
	Delete []*dns.Record
	Update []*UpdateChange
}

// Plan represents the reconciliation difference between the current
// and desired state of DNS records.
type Plan struct {
	// Current is the list of records as they exist in the provider.
	Current []*dns.Record
	// Desired is the list of records computed by the controller.
	Desired []*dns.Record
	// Changes is the computed set of actions required.
	Changes *Changes
}

// recordKey generates a unique identifier (Name:Type) for a DNS record
// to use in a map.
func recordKey(r *dns.Record) string {
	if r == nil {
		return ""
	}
	// DNS record names are generally case-insensitive, but types are case-sensitive.
	// We normalize both for consistent map keys.
	return fmt.Sprintf("%s:%s", strings.ToLower(r.Name), strings.ToUpper(r.Type))
}

// recordsAreEqual checks if two records are semantically equivalent.
// It compares TTL, Values (order-agnostic), Priority, Weight, and Port.
func recordsAreEqual(a, b *dns.Record) bool {
	if a.TTL != b.TTL {
		return false
	}

	// Compare nil-ness and values of pointers
	if !reflect.DeepEqual(a.Priority, b.Priority) {
		return false
	}
	if !reflect.DeepEqual(a.Weight, b.Weight) {
		return false
	}
	if !reflect.DeepEqual(a.Port, b.Port) {
		return false
	}

	// Compare Values. Order should not matter.
	if len(a.Values) != len(b.Values) {
		return false
	}

	// Sort copies of the slices to compare content deterministically.
	aValues := make([]string, len(a.Values))
	copy(aValues, a.Values)
	sort.Strings(aValues)

	bValues := make([]string, len(b.Values))
	copy(bValues, b.Values)
	sort.Strings(bValues)

	return reflect.DeepEqual(aValues, bValues)
}

// Calculate computes the delta between the current and desired record sets.
//
// It populates the Changes struct with:
//   - **Create**: Records present in Desired but not in Current.
//   - **Update**: Records present in both, but with different content (TTL, Values, etc.).
//   - **Delete**: Records present in Current but not in Desired.
//
// It returns a new Plan object with the computed Changes section populated.
func (p *Plan) Calculate() *Plan {
	if p.Current == nil || p.Desired == nil {
		log.Errorf("plan calculation failed: both Current [%v] and Desired [%v] slices must be non-nil:", p.Current, p.Desired)
		return p
	}

	log.Debugf("Calculating reconciliation plan. Current records=%d, Desired records=%d", len(p.Current), len(p.Desired))

	changes := &Changes{}

	// Use maps for efficient O(N) comparison
	currentMap := make(map[string]*dns.Record)
	for _, r := range p.Current {
		key := recordKey(r)
		if _, exists := currentMap[key]; exists {
			log.Warnf("Duplicate record found in Current state: %s. Overwriting in planner.", key)
		}
		currentMap[key] = r
	}

	desiredMap := make(map[string]*dns.Record)
	for _, r := range p.Desired {
		key := recordKey(r)
		if _, exists := desiredMap[key]; exists {
			log.Warnf("Duplicate record found in Desired state: %s. Overwriting in planner.", key)
		}
		desiredMap[key] = r
	}

	// --- 1. Find Creates and Updates ---
	// Iterate over the desired state.
	for key, desired := range desiredMap {
		current, exists := currentMap[key]

		if !exists {
			// Not in Current, so it's a Create.
			changes.Create = append(changes.Create, desired)
		} else {
			// Exists in both. Check for differences.
			if !recordsAreEqual(current, desired) {
				// Records differ, so it's an Update.
				changes.Update = append(changes.Update, &UpdateChange{
					Current: current,
					Desired: desired,
				})
			}
		}
	}

	// --- 2. Find Deletes ---
	// Iterate over the current state.
	for key, current := range currentMap {
		if _, exists := desiredMap[key]; !exists {
			// In Current, but not in Desired, so it's a Delete.
			changes.Delete = append(changes.Delete, current)
		}
	}

	// Return a new plan with the original inputs and computed changes.
	newPlan := &Plan{
		Current: p.Current, // Note: these are pointers to the original slices
		Desired: p.Desired,
		Changes: changes,
	}

	log.Debugf("Plan calculated. Create=%d, Update=%d, Delete=%d",
		len(changes.Create), len(changes.Update), len(changes.Delete))

	return newPlan
}

// --- String Helpers for Logging ---

// recordToString provides a compact string for a single record.
func recordToString(r *dns.Record) string {
	if r == nil {
		return "<nil>"
	}
	// Sort values for deterministic output
	vals := make([]string, len(r.Values))
	copy(vals, r.Values)
	sort.Strings(vals)
	return fmt.Sprintf("%s %d %s %v", r.Name, r.TTL, r.Type, vals)
}

// recordSliceToString creates a summary of a record slice.
func recordSliceToString(records []*dns.Record) string {
	if len(records) == 0 {
		return "{}"
	}
	parts := make([]string, len(records))
	for i, r := range records {
		parts[i] = recordToString(r)
	}
	return fmt.Sprintf("{%s}", strings.Join(parts, ", "))
}

// updateChangeSliceToString creates a summary of an update slice.
func updateChangeSliceToString(updates []*UpdateChange) string {
	if len(updates) == 0 {
		return "{}"
	}
	parts := make([]string, len(updates))
	for i, u := range updates {
		parts[i] = fmt.Sprintf("(From: %s, To: %s)", recordToString(u.Current), recordToString(u.Desired))
	}
	return fmt.Sprintf("{%s}", strings.Join(parts, ", "))
}

// ToString returns a single-line string representation of the Plan,
// including Current, Desired, and Changes (Create/Update/Delete).
func (p *Plan) ToString() string {
	var sb strings.Builder

	sb.WriteString("Plan{")

	sb.WriteString("Current=")
	sb.WriteString(recordSliceToString(p.Current))
	sb.WriteString(", ")

	sb.WriteString("Desired=")
	sb.WriteString(recordSliceToString(p.Desired))
	sb.WriteString(", ")

	sb.WriteString("Changes={")
	if p.Changes != nil {
		sb.WriteString("Create=")
		sb.WriteString(recordSliceToString(p.Changes.Create))
		sb.WriteString(", Update=")
		sb.WriteString(updateChangeSliceToString(p.Changes.Update))
		sb.WriteString(", Delete=")
		sb.WriteString(recordSliceToString(p.Changes.Delete))
	} else {
		sb.WriteString("<nil>")
	}
	sb.WriteString("}") // close Changes

	sb.WriteString("}") // close Plan

	return sb.String()
}
