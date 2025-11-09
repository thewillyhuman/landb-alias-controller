package provider

import (
	"gitlab.cern.ch/gfacundo/landb-alias-controller/dns"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/plan" // Added import
)

// Provider defines the interface that any DNS provider implementation must
// satisfy.
//
// A DNS provider is any system capable of managing DNS records for one or more
// zones. The Provider interface is designed to be **provider-agnostic**,
// meaning implementations can target AWS Route53, Cloudflare, GCP Cloud DNS,
// on-prem DNS servers, or any other system capable of storing and serving DNS
// records.
//
// Implementations of this interface are expected to operate with the generic
// dns.Record type defined in the dns package.
//
// Error Handling:
//   - If any operation fails (e.g., network errors, permission errors,
//     validation errors, provider-specific API errors), the methods should
//     return a **non-nil error**.
//   - Implementations should wrap provider-specific errors with context so that
//     callers can identify the source of the failure.
//
// Thread Safety:
//   - Implementations should document whether they are safe for concurrent use.
type Provider interface {
	// Records retrieves the current DNS records from the provider.
	//
	// Returns:
	//   - []*dns.Record: slice of pointers to dns.Record representing the current
	//     state of DNS records managed by this provider.
	//   - error: non-nil if the operation fails. Errors may be network errors,
	//     provider API errors, authentication errors, or validation errors.
	//
	// Notes:
	//   - Records returned should be fully populated, including Name, Type, TTL,
	//     Values, and any optional fields such as Priority, Weight, Port,
	//     Provider, ProviderID, Meta.
	Records() ([]*dns.Record, error)

	// Reconcile applies a calculated set of changes to the provider's
	// DNS records.
	//
	// This method is responsible for executing the actions defined in the
	// plan.Changes struct: creating, updating, and deleting records
	// as specified.
	//
	// Parameters:
	//   - changes: A pointer to the plan.Changes struct containing the
	//     lists of records to create, update, and delete.
	//
	// Returns:
	//   - error: non-nil if any of the create, update, or delete
	//     operations fail.
	//
	// Notes:
	//   - Implementations should aim to execute the changes as efficiently
	//     as possible, using batch operations if the provider's API
	//     supports them.
	//   - If the provider API supports partial success (e.g., 2 of 3
	//     deletes succeed), the implementation should decide whether to
	//     return an error. Returning an error is recommended if any
	//     part of the plan fails to apply.
	Reconcile(changes *plan.Changes) error
}
