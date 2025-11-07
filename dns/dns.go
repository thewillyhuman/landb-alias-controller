package dns

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Record represents a generic, provider-agnostic DNS record.
//
// This struct is designed to abstract away provider-specific differences while
// maintaining a consistent model for all DNS records across multiple providers.
//
// **Key Principles**
//  1. **Immutability:** Once a Record is created, it should **never be modified**.
//     If a change is required (e.g., TTL change, value update, type change), a
//     new Record must be created instead. This ensures consistency across
//     providers and avoids accidental mutation of shared references.
//  2. **Provider-Agnostic:** All fields are designed to be generic and applicable
//     to any DNS provider. Optional fields (Priority, Weight, Port, ProviderID, Meta)
//     are included to accommodate provider-specific functionality without
//     breaking the abstraction.
//  3. **Validation:** Records should always be validated using `Validate()`
//     before being stored, sent to a provider, or compared. This ensures that
//     Names are valid FQDNs, Types are supported, TTL is positive, Values are
//     correctly formatted, and optional fields are only set when appropriate.
//
// **Fields**
type Record struct {
	// ID is an internal unique identifier for the record within the system.
	// It is not intended to be used by external providers. Each record should have
	// a unique ID even if multiple records have the same Name and Type.
	ID string `json:"id"`

	// Name is the fully qualified domain name (FQDN) for this record, e.g., "www.example.com".
	// Must be a valid FQDN.
	Name string `json:"name"`

	// Type indicates the DNS record type: A, AAAA, CNAME, MX, TXT, SRV, NS, etc.
	// Must be one of the supported DNS types.
	Type string `json:"type"`

	// TTL (Time-To-Live) in seconds. Must be positive.
	TTL int `json:"ttl"`

	// Values holds the content of the record. The interpretation depends on the Type:
	//   - A/AAAA: list of IP addresses
	//   - CNAME/NS/MX/SRV: list of domain names
	//   - TXT: list of text strings
	//   - SRV: list of target hosts
	Values []string `json:"values"`

	// Priority is used only for MX or SRV records. Nil otherwise.
	Priority *int `json:"priority,omitempty"`

	// Weight is used only for SRV records. Nil otherwise.
	Weight *int `json:"weight,omitempty"`

	// Port is used only for SRV records. Nil otherwise.
	Port *int `json:"port,omitempty"`

	// Provider is an optional name of the provider that manages this record,
	// e.g., "route53", "cloudflare". Nil if the record is generic or not yet
	// assigned to a provider.
	Provider *string `json:"provider,omitempty"`

	// ProviderID is an optional provider-specific identifier for the record.
	// It should never be modified manually and is only for tracking the record
	// within the provider.
	ProviderID *string `json:"provider_id,omitempty"`

	// Meta is a flexible map for storing provider-specific or additional metadata.
	// Can include routing policies, tags, or other custom fields. Should not
	// include any critical fields that conflict with Name, Type, TTL, or Values.
	Meta map[string]string `json:"meta,omitempty"`
}

// valid DNS record types
var validTypes = map[string]struct{}{
	"A": {}, "AAAA": {}, "CNAME": {}, "MX": {}, "TXT": {}, "SRV": {}, "NS": {},
}

// IsValid returns true if the Record is well-formed according to DNS rules.
// Internally, it calls Validate() and ignores the error message.
// This is useful for quick checks where detailed error information is not needed.
func (r *Record) IsValid() bool {
	return r.Validate() == nil
}

// Validate checks if the Record is well-formed according to DNS rules.
// Returns nil if valid, otherwise an error describing the problem.
//
// Validation Rules:
//  1. Name must be a non-empty valid FQDN.
//  2. Type must be one of the supported DNS record types (A, AAAA, CNAME, MX, TXT, SRV, NS).
//  3. TTL must be a positive integer.
//  4. Values must be non-empty and type-appropriate:
//     - A: IPv4 addresses
//     - AAAA: IPv6 addresses
//     - CNAME/NS/MX/SRV: valid FQDNs
//     - TXT: non-empty strings
//  5. Priority is only allowed for MX and SRV records.
//  6. Weight and Port are only allowed for SRV records.
//  7. Meta keys and values must be non-empty strings.
//
// Notes:
//   - This function does not modify the Record; it only validates.
//   - Any change to the record requires creating a new Record instance.
func (r *Record) Validate() error {
	if r == nil {
		return errors.New("record is nil")
	}

	// Name validation
	if r.Name == "" {
		return errors.New("name cannot be empty")
	}
	if !isValidFQDN(r.Name) {
		return fmt.Errorf("invalid FQDN: %s", r.Name)
	}

	// Type validation
	if _, ok := validTypes[strings.ToUpper(r.Type)]; !ok {
		return fmt.Errorf("invalid record type: %s", r.Type)
	}

	// TTL validation
	if r.TTL <= 0 {
		return fmt.Errorf("TTL must be positive: %d", r.TTL)
	}

	// Values validation
	if len(r.Values) == 0 {
		return errors.New("values cannot be empty")
	}

	switch strings.ToUpper(r.Type) {
	case "A":
		for _, v := range r.Values {
			ip := net.ParseIP(v)
			if ip == nil || ip.To4() == nil {
				return fmt.Errorf("invalid IPv4 address in A record: %s", v)
			}
		}
	case "AAAA":
		for _, v := range r.Values {
			ip := net.ParseIP(v)
			if ip == nil || ip.To16() == nil || ip.To4() != nil {
				return fmt.Errorf("invalid IPv6 address in AAAA record: %s", v)
			}
		}
	case "CNAME", "MX", "NS", "SRV":
		for _, v := range r.Values {
			if !isValidFQDN(v) {
				return fmt.Errorf("invalid domain in %s record: %s", r.Type, v)
			}
		}
	case "TXT":
		for _, v := range r.Values {
			if v == "" {
				return errors.New("TXT record cannot have empty string values")
			}
		}
	}

	// Optional fields validation
	if r.Priority != nil && strings.ToUpper(r.Type) != "MX" && strings.ToUpper(r.Type) != "SRV" {
		return fmt.Errorf("priority only allowed for MX or SRV records")
	}
	if r.Weight != nil && strings.ToUpper(r.Type) != "SRV" {
		return fmt.Errorf("weight only allowed for SRV records")
	}
	if r.Port != nil && strings.ToUpper(r.Type) != "SRV" {
		return fmt.Errorf("port only allowed for SRV records")
	}

	// Meta validation (optional, but keys/values must be non-empty if present)
	for k, v := range r.Meta {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return fmt.Errorf("meta contains empty key or value: %q=%q", k, v)
		}
	}

	return nil
}

// helper: simple FQDN validation
func isValidFQDN(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if len(name) > 253 {
		return false
	}
	parts := strings.Split(name, ".")
	fqdnLabel := regexp.MustCompile(`^[a-zA-Z0-9-]{1,63}$`)
	for _, p := range parts {
		if !fqdnLabel.MatchString(p) || strings.HasPrefix(p, "-") || strings.HasSuffix(p, "-") {
			return false
		}
	}
	return true
}
