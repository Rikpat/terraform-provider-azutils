package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	internalvalidator "github.com/rikpat/terraform-provider-azutils/internal/validator"
)

var _ ephemeral.EphemeralResource = &IndependentTokenEphemeralResource{}

func NewIndependentTokenEphemeralResource() ephemeral.EphemeralResource {
	return &IndependentTokenEphemeralResource{}
}

type IndependentTokenEphemeralResource struct{}

type IndependentTokenEphemeralResourceModel struct {
	Cloud                       types.String `tfsdk:"cloud"`
	Credentials                 types.List   `tfsdk:"credentials"`
	AzurePipelinesCredential    types.Object `tfsdk:"azure_pipelines_credential"`
	ClientSecretCredential      types.Object `tfsdk:"client_secret_credential"`
	ClientCertificateCredential types.Object `tfsdk:"client_certificate_credential"`
	ManagedIdentityCredential   types.Object `tfsdk:"managed_identity_credential"`
	WorkloadIdentityCredential  types.Object `tfsdk:"workload_identity_credential"`
	Claims                      types.String `tfsdk:"claims"`
	EnableCAE                   types.Bool   `tfsdk:"enable_cae"`
	Scopes                      types.Set    `tfsdk:"scopes"`
	Token                       types.String `tfsdk:"token"`
}

func (r *IndependentTokenEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_independent_token"
}

func (r *IndependentTokenEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Fetches a Microsoft Entra ID access token using credentials configured in this ephemeral resource instead of provider credentials.",
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
			"claims": schema.StringAttribute{
				Description: "Additional claims required by a conditional access policy.",
				Optional:    true,
			},
			"enable_cae": schema.BoolAttribute{
				Description: "Enable Continuous Access Evaluation.",
				Optional:    true,
			},
			"scopes": schema.SetAttribute{
				MarkdownDescription: "Permission scopes required for the token.",
				Required:            true,
				ElementType:         types.StringType,
			},
			"token": schema.StringAttribute{
				Description: "Access token for the requested scopes.",
				Computed:    true,
				Sensitive:   true,
			},
		},
	}
}

func (r *IndependentTokenEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data IndependentTokenEphemeralResourceModel
	if resp.Diagnostics.Append(req.Config.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	credentialData := AzUtilsProviderModel{
		Cloud:                       data.Cloud,
		Credentials:                 data.Credentials,
		AzurePipelinesCredential:    data.AzurePipelinesCredential,
		ClientSecretCredential:      data.ClientSecretCredential,
		ClientCertificateCredential: data.ClientCertificateCredential,
		ManagedIdentityCredential:   data.ManagedIdentityCredential,
		WorkloadIdentityCredential:  data.WorkloadIdentityCredential,
	}
	providerData, diags := setupCredentialChain(ctx, &credentialData)
	if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
		return
	}

	token, tokenDiags := fetchToken(ctx, providerData.credential, data.Claims, data.EnableCAE, data.Scopes)
	if resp.Diagnostics.Append(tokenDiags...); resp.Diagnostics.HasError() {
		return
	}
	data.Token = types.StringValue(token)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
}
