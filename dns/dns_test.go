package dns

import (
	"testing"
)

//
// ─── BASIC VALIDATION AND ERROR CONDITIONS ─────────────────────────────────────
//

// TestValidate_NilRecord ensures that Validate() gracefully handles a nil receiver
// and returns an appropriate error rather than panicking.
func TestValidate_NilRecord(t *testing.T) {
	var r *Record
	if err := r.Validate(); err == nil {
		t.Error("expected error for nil record, got nil")
	}
}

// TestValidate_EmptyName verifies that a record with an empty Name field is rejected.
func TestValidate_EmptyName(t *testing.T) {
	r := &Record{Type: "A", TTL: 300, Values: []string{"1.1.1.1"}}
	if err := r.Validate(); err == nil || err.Error() != "name cannot be empty" {
		t.Errorf("expected 'name cannot be empty', got %v", err)
	}
}

// TestValidate_InvalidFQDN checks that names not conforming to FQDN syntax are invalid.
func TestValidate_InvalidFQDN(t *testing.T) {
	r := &Record{Name: "invalid_domain@", Type: "A", TTL: 300, Values: []string{"1.1.1.1"}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected invalid FQDN error, got nil")
	}
}

// TestValidate_InvalidType ensures unsupported record types are correctly rejected.
func TestValidate_InvalidType(t *testing.T) {
	r := &Record{Name: "example.com", Type: "WRONGTYPE", TTL: 300, Values: []string{"1.1.1.1"}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected invalid record type error, got nil")
	}
}

// TestValidate_InvalidTTL verifies that TTL must be a strictly positive integer.
func TestValidate_InvalidTTL(t *testing.T) {
	r := &Record{Name: "example.com", Type: "A", TTL: 0, Values: []string{"1.1.1.1"}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected TTL must be positive error, got nil")
	}
}

// TestValidate_EmptyValues ensures that a record cannot have an empty Values slice.
func TestValidate_EmptyValues(t *testing.T) {
	r := &Record{Name: "example.com", Type: "A", TTL: 300, Values: []string{}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected values cannot be empty error, got nil")
	}
}

//
// ─── TYPE-SPECIFIC VALIDATION ───────────────────────────────────────────────────
//

// TestValidate_ARecord_InvalidIPv4 ensures A records only contain valid IPv4 addresses.
func TestValidate_ARecord_InvalidIPv4(t *testing.T) {
	r := &Record{Name: "example.com", Type: "A", TTL: 300, Values: []string{"999.1.1.1"}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected invalid IPv4 error, got nil")
	}
}

// TestValidate_AAAARecord_InvalidIPv6 ensures AAAA records contain valid IPv6 addresses only.
func TestValidate_AAAARecord_InvalidIPv6(t *testing.T) {
	r := &Record{Name: "example.com", Type: "AAAA", TTL: 300, Values: []string{"1.1.1.1"}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected invalid IPv6 error, got nil")
	}
}

// TestValidate_CNAME_InvalidDomain ensures CNAME records only contain valid FQDN targets.
func TestValidate_CNAME_InvalidDomain(t *testing.T) {
	r := &Record{Name: "example.com", Type: "CNAME", TTL: 300, Values: []string{"invalid_domain@"}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected invalid domain in CNAME record, got nil")
	}
}

// TestValidate_TXT_EmptyString ensures TXT records cannot have empty string values.
func TestValidate_TXT_EmptyString(t *testing.T) {
	r := &Record{Name: "example.com", Type: "TXT", TTL: 300, Values: []string{""}}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected TXT record empty string error, got nil")
	}
}

//
// ─── OPTIONAL FIELD CONSTRAINTS ─────────────────────────────────────────────────
//

// TestValidate_PriorityNotAllowed verifies that Priority is only allowed for MX or SRV records.
func TestValidate_PriorityNotAllowed(t *testing.T) {
	p := 10
	r := &Record{Name: "example.com", Type: "A", TTL: 300, Values: []string{"1.1.1.1"}, Priority: &p}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected priority not allowed for A record, got nil")
	}
}

// TestValidate_WeightNotAllowed verifies that Weight is only valid for SRV records.
func TestValidate_WeightNotAllowed(t *testing.T) {
	w := 5
	r := &Record{Name: "example.com", Type: "MX", TTL: 300, Values: []string{"mail.example.com"}, Weight: &w}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected weight not allowed for MX record, got nil")
	}
}

// TestValidate_PortNotAllowed verifies that Port is only valid for SRV records.
func TestValidate_PortNotAllowed(t *testing.T) {
	p := 80
	r := &Record{Name: "example.com", Type: "MX", TTL: 300, Values: []string{"mail.example.com"}, Port: &p}
	if err := r.Validate(); err == nil || err.Error() == "" {
		t.Error("expected port not allowed for MX record, got nil")
	}
}

//
// ─── META FIELD VALIDATION ──────────────────────────────────────────────────────
//

// TestValidate_MetaEmptyKey ensures Meta map cannot contain empty keys.
func TestValidate_MetaEmptyKey(t *testing.T) {
	r := &Record{
		Name:   "example.com",
		Type:   "A",
		TTL:    300,
		Values: []string{"1.1.1.1"},
		Meta:   map[string]string{"": "value"},
	}
	if err := r.Validate(); err == nil {
		t.Error("expected meta with empty key error, got nil")
	}
}

// TestValidate_MetaEmptyValue ensures Meta map cannot contain empty string values.
func TestValidate_MetaEmptyValue(t *testing.T) {
	r := &Record{
		Name:   "example.com",
		Type:   "A",
		TTL:    300,
		Values: []string{"1.1.1.1"},
		Meta:   map[string]string{"key": ""},
	}
	if err := r.Validate(); err == nil {
		t.Error("expected meta with empty value error, got nil")
	}
}

//
// ─── POSITIVE VALIDATION SCENARIOS ──────────────────────────────────────────────
//

// TestValidate_ValidARecord ensures a standard A record passes validation.
func TestValidate_ValidARecord(t *testing.T) {
	r := &Record{Name: "www.example.com", Type: "A", TTL: 300, Values: []string{"8.8.8.8"}}
	if err := r.Validate(); err != nil {
		t.Errorf("expected valid A record, got error: %v", err)
	}
}

// TestValidate_ValidMXRecord ensures MX records with priority and valid target are accepted.
func TestValidate_ValidMXRecord(t *testing.T) {
	p := 10
	r := &Record{Name: "example.com", Type: "MX", TTL: 300, Values: []string{"mail.example.com"}, Priority: &p}
	if err := r.Validate(); err != nil {
		t.Errorf("expected valid MX record, got error: %v", err)
	}
}

// TestValidate_ValidSRVRecord ensures SRV records with priority, weight, and port are valid.
func TestValidate_ValidSRVRecord(t *testing.T) {
	p := 10
	w := 5
	port := 8080
	r := &Record{Name: "example.com", Type: "SRV", TTL: 300, Values: []string{"service.example.com"}, Priority: &p, Weight: &w, Port: &port}
	if err := r.Validate(); err != nil {
		t.Errorf("expected valid SRV record, got error: %v", err)
	}
}

//
// ─── ISVALID SHORTCUT TEST ──────────────────────────────────────────────────────
//

// TestIsValid_Shortcut checks that IsValid() correctly reflects the outcome of Validate().
func TestIsValid_Shortcut(t *testing.T) {
	r := &Record{Name: "www.example.com", Type: "A", TTL: 300, Values: []string{"8.8.8.8"}}
	if !r.IsValid() {
		t.Error("expected record to be valid, IsValid returned false")
	}

	r2 := &Record{Name: "bad@", Type: "A", TTL: 300, Values: []string{"8.8.8.8"}}
	if r2.IsValid() {
		t.Error("expected record to be invalid, IsValid returned true")
	}
}
