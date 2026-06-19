package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type AzurePipelinesCredentialModel[T types.String | string] struct {
	TenantID            T `tfsdk:"tenant_id" env:"ARM_TENANT_ID,AZURE_TENANT_ID"`
	ClientID            T `tfsdk:"client_id" env:"ARM_CLIENT_ID,AZURE_CLIENT_ID" missing:"warn"`
	ServiceConnectionID T `tfsdk:"service_connection_id" env:"ARM_OIDC_AZURE_SERVICE_CONNECTION_ID,AZURESUBSCRIPTION_SERVICE_CONNECTION_ID" missing:"warn"`
	SystemAccessToken   T `tfsdk:"system_access_token" env:"ARM_OIDC_REQUEST_TOKEN,SYSTEM_ACCESSTOKEN" missing:"warn"`
}
type APcM = AzurePipelinesCredentialModel[types.String] //model
type APcP = AzurePipelinesCredentialModel[string]       //parsed

type ClientSecretCredentialModel[T types.String | string] struct {
	TenantID     T `tfsdk:"tenant_id"`
	ClientID     T `tfsdk:"client_id"`
	ClientSecret T `tfsdk:"client_secret"`
}
type CScM = ClientSecretCredentialModel[types.String] //model
type CScP = ClientSecretCredentialModel[string]       //parsed

type ClientCertificateCredentialModel[T types.String | string] struct {
	TenantID            T `tfsdk:"tenant_id"`
	ClientID            T `tfsdk:"client_id"`
	CertificatePath     T `tfsdk:"certificate_path"`
	CertificatePassword T `tfsdk:"certificate_password"`
}
type CCcM = ClientCertificateCredentialModel[types.String] //model
type CCcP = ClientCertificateCredentialModel[string]       //parsed

type ManagedIdentityCredentialModel[T types.String | string] struct {
	ClientID T `tfsdk:"client_id"`
}
type MIcM = ManagedIdentityCredentialModel[types.String] //model
type MIcP = ManagedIdentityCredentialModel[string]       //parsed

type WorkloadIdentityCredentialModel[T types.String | string] struct {
	TenantID T `tfsdk:"tenant_id"`
	ClientID T `tfsdk:"client_id"`
}
type WIcM = WorkloadIdentityCredentialModel[types.String] //model
type WIcP = WorkloadIdentityCredentialModel[string]       //parsed

// AzutilsProviderModel describes the provider data model.
type AzUtilsProviderModel struct {
	Cloud                       types.String `tfsdk:"cloud"`
	Credentials                 types.List   `tfsdk:"credentials"`
	AzurePipelinesCredential    types.Object `tfsdk:"azure_pipelines_credential"`
	ClientSecretCredential      types.Object `tfsdk:"client_secret_credential"`
	ClientCertificateCredential types.Object `tfsdk:"client_certificate_credential"`
	ManagedIdentityCredential   types.Object `tfsdk:"managed_identity_credential"`
	WorkloadIdentityCredential  types.Object `tfsdk:"workload_identity_credential"`
}
