package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	internalvalidator "github.com/rikpat/terraform-provider-azutils/internal/validator"
)

var _ provider.Provider = &AzUtilsProvider{}
var _ provider.ProviderWithEphemeralResources = &AzUtilsProvider{}

// AzUtilsProvider defines the provider implementation.
type AzUtilsProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

func (p *AzUtilsProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "azutils"
	resp.Version = p.version
}

// Provider configuration is primarily about selecting and configuring credential sources.
func (p *AzUtilsProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `
Provider used for authenticating with resources supporting EntraID authentication.

Main usage is generating a token using Azure Pipelines Workload Federation Identity in IaC pipelines and falling back to azure_cli for local testing, but supports more credential types.

Most credentials have options like selecting client_id and tenant_id, except for *environment* and *azure_cli* credentials which take all the options from external sources.
		`,
		Attributes: map[string]schema.Attribute{
			"cloud": schema.StringAttribute{
				MarkdownDescription: "Cloud environment to target. Possible values are: ***AzurePublic*** (default), *AzureGovernment*, *AzureChina*",
				Optional:            true,
			},
			"credentials": schema.ListAttribute{
				ElementType: types.StringType,

				MarkdownDescription: `List of credentials to try. They will be tried in the specified order. 
	
	Supported types are: 
	- environment_credential
	- azure_pipelines_credential 
	- workload_identity_credential
	- managed_identity_credential
	- azure_cli_credential
	- client_secret_credential
	- client_certificate_credential`,
				Required: true,
				Validators: []validator.List{
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(
						stringvalidator.OneOf(
							"environment_credential",
							"azure_pipelines_credential",
							"workload_identity_credential",
							"managed_identity_credential",
							"azure_cli_credential",
							"client_secret_credential",
							"client_certificate_credential",
						),
						internalvalidator.ValueBased(map[string]validator.String{
							"client_secret_credential": stringvalidator.AlsoRequires(
								path.MatchRoot("client_secret_credential"),
							),
							"client_certificate_credential": stringvalidator.AlsoRequires(
								path.MatchRoot("client_certificate_credential"),
							),
						}),
					),
				},
			},
			"azure_pipelines_credential": schema.SingleNestedAttribute{
				MarkdownDescription: "Configuration block for Azure Pipelines Credential. If using TerraformTask@5, no configuration needed unless you want to use different service connection than used for terraform. If using AzureCLI@2 or AzurePowershell@5, you need to also set SYSTEM_ACCESSTOKEN env variable, or provide access token as terraform variable.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"tenant_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Optional tenant_id if it's different from used service connection (*ARM_TENANT_ID* or *AZURE_TENANT_ID*)",
					},
					"client_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Optional client_id if it's different from used service connection (*ARM_CLIENT_ID* or *AZURE_CLIENT_ID*)",
					},
					"service_connection_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Optional Azure DevOps Service Connection ID, if it's different from used service connection (*ARM_OIDC_AZURE_SERVICE_CONNECTION_ID* or *AZURESUBSCRIPTION_SERVICE_CONNECTION_ID*)",
					},
					"system_access_token": schema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "Optional OIDC request token, if not using Terraform@5 task, or not setting *SYSTEM_ACCESSTOKEN* env variable",
					},
				},
			},
			"workload_identity_credential": schema.SingleNestedAttribute{
				MarkdownDescription: "Configuration for workload identity credential. You can provide custom `client_id` and `tenant_id` if using multiple workload identities on single pod.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"tenant_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Optional override of tenant_id, if not using the identity specified in service account annotations (in *AZURE_TENANT_ID* env variable)",
					},
					"client_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Optional override of client_id, if not using the identity specified in service account annotations (in *AZURE_CLIENT_ID* env variable)"},
				},
			},
			"managed_identity_credential": schema.SingleNestedAttribute{
				MarkdownDescription: "Configuration for Managed Identity credential (optional `client_id` for user-assigned identity).",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"client_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Optional override of client_id, if using user-assigned identity",
					},
				},
			},
			"client_secret_credential": schema.SingleNestedAttribute{
				MarkdownDescription: "Configuration for a client secret credential. All properties are required, as there's already environment_credential that provides same functionality with env variables.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"tenant_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Tenant ID of the service principal",
					},
					"client_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Client ID of the service principal",
					},
					"client_secret": schema.StringAttribute{
						Required:            true,
						Sensitive:           true,
						MarkdownDescription: "Client Secret of the service principal",
					},
				},
			},
			"client_certificate_credential": schema.SingleNestedAttribute{
				MarkdownDescription: "Configuration for a client certificate credential. All properties (except password in case of unencrypted certificate) are required, as there's already environment_credential that provides same functionality with env variables.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"tenant_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Tenant ID of the service principal",
					},
					"client_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Client ID of the service principal",
					},
					"certificate_path": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Path to certificate used for authentication. Can be relative to current working directory (terraform root).",
					},
					"certificate_password": schema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "Password to certificate file, if used.",
					},
				},
			},
		},
	}
}

func (p *AzUtilsProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring provider")
	// Read all env vars

	var data AzUtilsProviderModel

	if resp.Diagnostics.Append(req.Config.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	cred, diags := setupCredentialChain(ctx, &data)

	if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
		return
	}

	resp.ResourceData = cred
	resp.EphemeralResourceData = cred
}

func (p *AzUtilsProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewPostgresqlEntraIDUser,
	}
}

func (p *AzUtilsProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewTokenEphemeralResource,
	}
}

func (p *AzUtilsProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &AzUtilsProvider{
			version: version,
		}
	}
}
