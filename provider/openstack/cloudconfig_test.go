package openstack

import (
	"context"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParseCloudConfig_ValidInput(t *testing.T) {
	input := []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
ca-file = "/etc/kubernetes/ca-bundle.crt"
password = "PjVH9tpHaCYaDd4nZx"
region = "cern"
trust-id = "b5f17e795efe439995b9c7f982f6e460"
user-id = "5f9112836737466091abbb6c24397f0c"

[Networking]
internal-network-name = "CERN_NETWORK"

[LoadBalancer]
cascade-delete = "true"
`)

	cfg, err := parseCloudConfig(input)
	require.NoError(t, err)
	assert.Equal(t, "https://keystone.cern.ch/v3", cfg.AuthOptions.IdentityEndpoint)
	assert.Equal(t, "5f9112836737466091abbb6c24397f0c", cfg.AuthOptions.UserID)
	assert.Equal(t, "PjVH9tpHaCYaDd4nZx", cfg.AuthOptions.Password)
	assert.Equal(t, "cern", cfg.Region)
	require.NotNil(t, cfg.AuthOptions.Scope)
	assert.Equal(t, "b5f17e795efe439995b9c7f982f6e460", cfg.AuthOptions.Scope.TrustID)
}

func TestParseCloudConfig_UnquotedValues(t *testing.T) {
	input := []byte(`[Global]
auth-url = https://keystone.cern.ch/v3
password = secret123
trust-id = abc123
user-id = def456
`)

	cfg, err := parseCloudConfig(input)
	require.NoError(t, err)
	assert.Equal(t, "https://keystone.cern.ch/v3", cfg.AuthOptions.IdentityEndpoint)
	assert.Equal(t, "secret123", cfg.AuthOptions.Password)
	assert.Equal(t, "abc123", cfg.AuthOptions.Scope.TrustID)
	assert.Equal(t, "def456", cfg.AuthOptions.UserID)
}

func TestParseCloudConfig_ExtraWhitespace(t *testing.T) {
	input := []byte(`  [Global]
  auth-url  =  "https://keystone.cern.ch/v3"
  password  =  "secret"
  trust-id  =  "tid"
  user-id  =  "uid"
`)

	cfg, err := parseCloudConfig(input)
	require.NoError(t, err)
	assert.Equal(t, "https://keystone.cern.ch/v3", cfg.AuthOptions.IdentityEndpoint)
	assert.Equal(t, "secret", cfg.AuthOptions.Password)
}

func TestParseCloudConfig_MissingAuthURL(t *testing.T) {
	input := []byte(`[Global]
password = "secret"
trust-id = "tid"
user-id = "uid"
`)

	_, err := parseCloudConfig(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-url")
}

func TestParseCloudConfig_MissingPassword(t *testing.T) {
	input := []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
trust-id = "tid"
user-id = "uid"
`)

	_, err := parseCloudConfig(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password")
}

func TestParseCloudConfig_MissingUserID(t *testing.T) {
	input := []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
password = "secret"
trust-id = "tid"
`)

	_, err := parseCloudConfig(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user-id")
}

func TestParseCloudConfig_MissingTrustID(t *testing.T) {
	input := []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
password = "secret"
user-id = "uid"
`)

	_, err := parseCloudConfig(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trust-id")
}

func TestParseCloudConfig_ApplicationCredential(t *testing.T) {
	input := []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
application-credential-id = "app-cred-id"
application-credential-secret = "app-cred-secret"
`)

	cfg, err := parseCloudConfig(input)
	require.NoError(t, err)
	assert.Equal(t, "https://keystone.cern.ch/v3", cfg.AuthOptions.IdentityEndpoint)
	assert.Equal(t, "app-cred-id", cfg.AuthOptions.ApplicationCredentialID)
	assert.Equal(t, "app-cred-secret", cfg.AuthOptions.ApplicationCredentialSecret)
}

func TestParseCloudConfig_MissingApplicationCredentialSecret(t *testing.T) {
	input := []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
application-credential-id = "app-cred-id"
`)

	_, err := parseCloudConfig(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "application-credential-secret")
}

func TestParseCloudConfig_EmptyInput(t *testing.T) {
	_, err := parseCloudConfig([]byte{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
}

func TestParseCloudConfig_IgnoresComments(t *testing.T) {
	input := []byte(`# This is a comment
[Global]
; Another comment
auth-url = "https://keystone.cern.ch/v3"
password = "secret"
trust-id = "tid"
user-id = "uid"
`)

	cfg, err := parseCloudConfig(input)
	require.NoError(t, err)
	assert.Equal(t, "https://keystone.cern.ch/v3", cfg.AuthOptions.IdentityEndpoint)
}

func TestParseCloudConfig_NoGlobalSection(t *testing.T) {
	input := []byte(`[Networking]
auth-url = "https://keystone.cern.ch/v3"
password = "secret"
trust-id = "tid"
user-id = "uid"
`)

	_, err := parseCloudConfig(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
}

func TestReadCloudConfigSecret_Success(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloud-config",
			Namespace: "kube-system",
		},
		Data: map[string][]byte{
			"cloud.conf": []byte(`[Global]
auth-url = "https://keystone.cern.ch/v3"
password = "secret"
trust-id = "tid"
user-id = "uid"
`),
		},
	}

	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	cfg, err := ReadCloudConfigSecret(context.Background(), fakeClient, "kube-system", "cloud-config")
	require.NoError(t, err)
	assert.Equal(t, "https://keystone.cern.ch/v3", cfg.AuthOptions.IdentityEndpoint)
	assert.Equal(t, "uid", cfg.AuthOptions.UserID)
	assert.Equal(t, "secret", cfg.AuthOptions.Password)
	require.NotNil(t, cfg.AuthOptions.Scope)
	assert.Equal(t, "tid", cfg.AuthOptions.Scope.TrustID)
}

func TestReadCloudConfigSecret_NotFound(t *testing.T) {
	fakeClient := fake.NewClientBuilder().Build()
	_, err := ReadCloudConfigSecret(context.Background(), fakeClient, "kube-system", "cloud-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading secret")
}

func TestReadCloudConfigSecret_MissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloud-config",
			Namespace: "kube-system",
		},
		Data: map[string][]byte{
			"other-key": []byte("data"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	_, err := ReadCloudConfigSecret(context.Background(), fakeClient, "kube-system", "cloud-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloud.conf")
}

func TestValidateAuthOptions_TrustBased(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint: "https://keystone.cern.ch/v3",
			UserID:           "uid",
			Password:         "secret",
			Scope:            &gophercloud.AuthScope{TrustID: "tid"},
		},
	}
	assert.NoError(t, p.validateAuthOptions())
}

func TestValidateAuthOptions_UsernameBased(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint: "https://keystone.cern.ch/v3",
			Username:         "user",
			Password:         "secret",
			TenantName:       "project",
			DomainName:       "Default",
		},
	}
	assert.NoError(t, p.validateAuthOptions())
}

func TestValidateAuthOptions_ApplicationCredential(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint:            "https://keystone.cern.ch/v3",
			ApplicationCredentialID:     "app-cred-id",
			ApplicationCredentialSecret: "app-cred-secret",
		},
	}
	assert.NoError(t, p.validateAuthOptions())
}

func TestValidateAuthOptions_ApplicationCredentialMissingSecret(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint:        "https://keystone.cern.ch/v3",
			ApplicationCredentialID: "app-cred-id",
		},
	}
	err := p.validateAuthOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "application-credential-secret")
}

func TestValidateAuthOptions_NoUserIdentity(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint: "https://keystone.cern.ch/v3",
			Password:         "secret",
		},
	}
	err := p.validateAuthOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user-id, username, or application-credential-id")
}

func TestValidateAuthOptions_TrustBasedMissingTrustID(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint: "https://keystone.cern.ch/v3",
			UserID:           "uid",
			Password:         "secret",
		},
	}
	err := p.validateAuthOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trust-id")
}

func TestValidateAuthOptions_UsernameBasedMissingDomain(t *testing.T) {
	p := &Provider{
		authOpts: gophercloud.AuthOptions{
			IdentityEndpoint: "https://keystone.cern.ch/v3",
			Username:         "user",
			Password:         "secret",
			TenantName:       "project",
		},
	}
	err := p.validateAuthOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "domain-name")
}
