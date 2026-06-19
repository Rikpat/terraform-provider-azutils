package provider

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ ephemeral.EphemeralResource = &TokenEphemeralResource{}

func NewTokenEphemeralResource() ephemeral.EphemeralResource {
	return &TokenEphemeralResource{}
}

// TokenEphemeralResource defines the ephemeral resource implementation.
type TokenEphemeralResource struct {
	credential *azidentity.ChainedTokenCredential
}

// TokenEphemeralResourceModel describes the ephemeral resource data model.
type TokenEphemeralResourceModel struct {
	// Output
	Token types.String `tfsdk:"token"`
	// Inputs
	Claims    types.String `tfsdk:"claims"`
	EnableCAE types.Bool   `tfsdk:"enable_cae"`
	Scopes    types.Set    `tfsdk:"scopes"`
}

func (r *TokenEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_token"
}

func (r *TokenEphemeralResource) Schema(ctx context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Fetches Microsoft login access token to be used with different resources (ex. databases) using credentials configured in provider.",
		Attributes: map[string]schema.Attribute{
			"claims": schema.StringAttribute{
				Description: "Any additional claims required for the token to satisfy a conditional access policy, such as a service may return in a claims challenge following an authorization failure.",
				Optional:    true,
			},
			"enable_cae": schema.BoolAttribute{
				Description: "Indicates whether to enable Continuous Access Evaluation (CAE) for the requested token. Requires a client supporting CAE. The default is false.",
				Optional:    true,
			},
			"scopes": schema.SetAttribute{
				MarkdownDescription: "List of permission scopes required for the token, ex. `https://ossrdbms-aad.database.windows.net/.default` for relational databases. Although a list is supported, it's probably better to use separate tokens for separate scopes.",
				Required:            true,
				ElementType:         types.StringType,
			},
			"token": schema.StringAttribute{
				Description: "Output token for required scopes",
				Computed:    true,
				Sensitive:   true,
			},
		},
	}
}

func (d *TokenEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	// Always perform a nil check when handling ProviderData because Terraform
	// sets that data after it calls the ConfigureProvider RPC.
	if req.ProviderData == nil {
		return
	}

	credential, ok := req.ProviderData.(*azidentity.ChainedTokenCredential)

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Ephemeral Resource Configure Type",
			fmt.Sprintf("Expected *azidentity.ChainedTokenCredential, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.credential = credential
}

func (r *TokenEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data TokenEphemeralResourceModel

	// Read Terraform config data into the model
	if resp.Diagnostics.Append(req.Config.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	// Parse scopes
	scopes := make([]string, 0, len(data.Scopes.Elements()))
	diags := data.Scopes.ElementsAs(ctx, &scopes, false)
	if resp.Diagnostics.Append(diags...); diags.HasError() {
		return
	}

	token, err := r.credential.GetToken(ctx, policy.TokenRequestOptions{
		Claims:    data.Claims.ValueString(),
		Scopes:    scopes,
		EnableCAE: data.EnableCAE.ValueBool(),
	})

	if err != nil {
		resp.Diagnostics.AddError("Unable to get token", err.Error())
		return
	}

	data.Token = types.StringValue(token.Token)

	// Save data into ephemeral result data
	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
}
