package provider

import "gitlab.cern.ch/gfacundo/landb-alias-controller/dns"

// Provider defines the interface that any DNS provider implementation must satisfy.
//
// A DNS provider is any system capable of managing DNS records for one or more zones.
// The Provider interface is designed to be **provider-agnostic**, meaning implementations
// can target AWS Route53, Cloudflare, GCP Cloud DNS, on-prem DNS servers, or any other
// system capable of storing and serving DNS records.
//
// Implementations of this interface are expected to operate with the generic
// dns.Record type defined in the dns package. All records passed to or returned
// from this interface should be validated using dns.Record.Validate().
//
// Error Handling:
//   - If any operation fails (e.g., network errors, permission errors, validation errors,
//     provider-specific API errors), the methods should return a **non-nil error**.
//   - Implementations should wrap provider-specific errors with context so that callers
//     can identify the source of the failure.
//
// Thread Safety:
//   - Implementations should document whether they are safe for concurrent use.
//
// Notes:
//   - The provider is not responsible for automatically merging or deduplicating records;
//     it should store exactly the records passed to SetRecords().
//   - The provider may choose to preserve its internal record IDs, but the equality of
//     records for logical purposes should be based on dns.Record.Equals().
type Provider interface {
	// CurrentRecords retrieves the current DNS records from the provider.
	//
	// Returns:
	//   - []*dns.Record: slice of pointers to dns.Record representing the current
	//     state of DNS records managed by this provider.
	//   - error: non-nil if the operation fails. Errors may be network errors,
	//     provider API errors, authentication errors, or validation errors.
	//
	// Notes:
	//   - Records returned should be fully populated, including Name, Type, TTL, Values,
	//     and any optional fields such as Priority, Weight, Port, Provider, ProviderID, Meta.
	//   - The caller can safely call dns.Record.Equals() to compare returned records
	//     with other records.
	CurrentRecords() ([]*dns.Record, error)

	// SetRecords updates the DNS records on the provider to exactly match the provided slice.
	//
	// Records not present in the slice should be deleted from the provider if they exist.
	// Records in the slice that do not exist in the provider should be created.
	// Records that exist in the provider and match the slice (by dns.Record.Equals()) may
	// be left unchanged, depending on provider implementation.
	//
	// Parameters:
	//   - records: slice of pointers to dns.Record that represents the desired state.
	//
	// Returns:
	//   - error: non-nil if the operation fails. Errors may include:
	//       * Validation errors: e.g., records failing dns.Record.Validate()
	//       * Network or API errors communicating with the provider
	//       * Permission or authentication errors
	//
	// Notes:
	//   - Implementations should perform **atomic or idempotent updates** whenever possible.
	//   - Implementations may choose to log or return partial failures if the provider
	//     API allows batch updates with partial errors.
	SetRecords(records []*dns.Record) error
}
