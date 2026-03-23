package openstack

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// parseCloudConfig parses an INI-format cloud.conf file and extracts
// OpenStack authentication options. Only the [Global] section is read;
// other sections are ignored.
//
// Expected keys: auth-url, user-id, password, trust-id.
func parseCloudConfig(data []byte) (gophercloud.AuthOptions, error) {
	var opts gophercloud.AuthOptions
	inGlobal := false

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Section headers.
		if strings.HasPrefix(line, "[") {
			inGlobal = strings.EqualFold(line, "[global]")
			continue
		}

		if !inGlobal {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)

		switch key {
		case "auth-url":
			opts.IdentityEndpoint = value
		case "user-id":
			opts.UserID = value
		case "password":
			opts.Password = value
		case "trust-id":
			if opts.Scope == nil {
				opts.Scope = &gophercloud.AuthScope{}
			}
			opts.Scope.TrustID = value
		}
	}

	if err := scanner.Err(); err != nil {
		return gophercloud.AuthOptions{}, fmt.Errorf("reading cloud config: %w", err)
	}

	// Validate required fields.
	var missing []string
	if opts.IdentityEndpoint == "" {
		missing = append(missing, "auth-url")
	}
	if opts.UserID == "" {
		missing = append(missing, "user-id")
	}
	if opts.Password == "" {
		missing = append(missing, "password")
	}
	if opts.Scope == nil || opts.Scope.TrustID == "" {
		missing = append(missing, "trust-id")
	}
	if len(missing) > 0 {
		return gophercloud.AuthOptions{}, fmt.Errorf("cloud config missing required fields: %s",
			strings.Join(missing, ", "))
	}

	return opts, nil
}

// ReadCloudConfigSecret reads the cloud-config Secret from the given
// namespace and parses it into OpenStack authentication options.
func ReadCloudConfigSecret(ctx context.Context, reader client.Reader, namespace, name string) (gophercloud.AuthOptions, error) {
	var secret corev1.Secret
	nn := types.NamespacedName{Namespace: namespace, Name: name}
	if err := reader.Get(ctx, nn, &secret); err != nil {
		return gophercloud.AuthOptions{}, fmt.Errorf("reading secret %s/%s: %w", namespace, name, err)
	}

	data, ok := secret.Data["cloud.conf"]
	if !ok {
		return gophercloud.AuthOptions{}, fmt.Errorf("secret %s/%s has no 'cloud.conf' key", namespace, name)
	}

	return parseCloudConfig(data)
}
