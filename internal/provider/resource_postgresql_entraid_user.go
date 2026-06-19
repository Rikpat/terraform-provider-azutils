package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int32default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type postgresqlEntraIDUserModel struct {
	Server postgresqlEntraIDUserServerModel `tfsdk:"server"`
	User   postgresqlEntraIDUserUserModel   `tfsdk:"user"`
}

type postgresqlEntraIDUserServerModel struct {
	Host          types.String `tfsdk:"host"`
	Port          types.Int32  `tfsdk:"port"`
	AdminUsername types.String `tfsdk:"admin_username"`
	AdminPassword types.String `tfsdk:"admin_password"`
}

type postgresqlEntraIDUserUserModel struct {
	Name       types.String `tfsdk:"name"`
	ObjectID   types.String `tfsdk:"object_id"`
	ObjectType types.String `tfsdk:"object_type"`
	IsAdmin    types.Bool   `tfsdk:"is_admin"`
}

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource              = &postgresqlEntraIDUserResource{}
	_ resource.ResourceWithConfigure = &postgresqlEntraIDUserResource{}
)

// NewPostgresqlEntraIDUser is a helper function to simplify the provider implementation.
func NewPostgresqlEntraIDUser() resource.Resource {
	return &postgresqlEntraIDUserResource{}
}

// postgresqlEntraIDUserResource is the resource implementation.
type postgresqlEntraIDUserResource struct {
	credential *azidentity.ChainedTokenCredential
}

// Metadata returns the resource type name.
func (r *postgresqlEntraIDUserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_postgresql_entraid_user"
}

func (r *postgresqlEntraIDUserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	credential, ok := req.ProviderData.(*azidentity.ChainedTokenCredential)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *azidentity.ChainedTokenCredential, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.credential = credential
}

// Schema defines the schema for the resource.
func (r *postgresqlEntraIDUserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Creates a PostgreSQL Role for EntraID user, group or service principal. Can create a global admin, but rest of the permissions should be managed using a different postgresql specific provider (using ephemeral token for password).",
		Attributes: map[string]schema.Attribute{
			"server": schema.SingleNestedAttribute{
				MarkdownDescription: "The PostgreSQL server connection details.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"host": schema.StringAttribute{
						MarkdownDescription: "The hostname or IP of the PostgreSQL server.",
						Required:            true,
					},
					"port": schema.Int32Attribute{
						MarkdownDescription: "The port of the PostgreSQL server. Defaults to 5432.",
						Optional:            true,
						Computed:            true,
						Default:             int32default.StaticInt32(5432),
					},
					"admin_username": schema.StringAttribute{
						MarkdownDescription: "The username of admin user used for creating the new user. If using EntraID authentication, this should be the name of the currently authenticated user/service principal.",
						Required:            true,
					},
					"admin_password": schema.StringAttribute{
						MarkdownDescription: "Password of the admin user used for creating the new user. If not set, will attempt to use EntraID authentication.",
						Optional:            true,
						Sensitive:           true,
					},
				},
			},
			"user": schema.SingleNestedAttribute{
				MarkdownDescription: "Specifies the EntraID user to be created in PostgreSQL. Internally the resource is using [pgaadauth_create_principal](https://learn.microsoft.com/en-us/azure/postgresql/security/security-manage-entra-users#create-a-user-or-role-with-a-microsoft-entra-principal-name) when object_id is not set, [pgaadauth_create_principal_with_oid](https://learn.microsoft.com/en-us/azure/postgresql/security/security-manage-entra-users#create-a-role-using-the-microsoft-entra-id-object-identifier) otherwise.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						MarkdownDescription: "The name of the user to be created. This is used for signing in. If not using `object_id`, needs to match user, group or service principal name in EntraID tenant.",
						Required:            true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"object_id": schema.StringAttribute{
						MarkdownDescription: "The EntraID Object ID of the user, group or service principal. If set, `name` can be different from the actual name in EntraID, for example you can use service principal client id to not have to store actual name of principal anywhere. If set, also requires `object_type` to be set.",
						Optional:            true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.AlsoRequires(path.MatchRoot("user").AtName("object_type")),
						},
					},
					"object_type": schema.StringAttribute{
						MarkdownDescription: "The EntraID Object Type of the user, group or service principal. Is required if `object_id` is set. Valid values are `user`, `group` and `service`.",
						Optional:            true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
						Validators: []validator.String{
							stringvalidator.AlsoRequires(path.MatchRoot("user").AtName("object_id")),
						},
					},
					"is_admin": schema.BoolAttribute{
						MarkdownDescription: "Whether the user is an admin. Default is `false`. If you want to create an admin user, you should probably use `azurerm_postgresql_flexible_server_active_directory_administrator`, this does the same thing on database end intead of azure end.",
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(false),
					},
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *postgresqlEntraIDUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data postgresqlEntraIDUserModel

	if resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	if !data.User.ObjectID.IsNull() && !data.User.ObjectID.IsUnknown() {
		if data.User.ObjectType.IsNull() || data.User.ObjectType.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("user").AtName("object_type"),
				"Missing object type",
				"`user.object_type` is required when `user.object_id` is set.",
			)
			return
		}

		objectType := strings.ToLower(data.User.ObjectType.ValueString())
		if objectType != "user" && objectType != "group" && objectType != "service" {
			resp.Diagnostics.AddAttributeError(
				path.Root("user").AtName("object_type"),
				"Invalid object type",
				"`user.object_type` must be one of: user, group, service.",
			)
			return
		}
	}

	db, err := r.openDB(ctx, data)
	if err != nil {
		resp.Diagnostics.AddError("Unable to connect to PostgreSQL", err.Error())
		return
	}
	defer db.Close(ctx)

	if objectID := data.User.ObjectID.ValueString(); objectID != "" {
		_, err = db.Exec(
			ctx,
			"SELECT pgaadauth_create_principal_with_oid($1, $2, $3, $4, $5);",
			data.User.Name.ValueString(),
			objectID,
			strings.ToLower(data.User.ObjectType.ValueString()),
			data.User.IsAdmin.ValueBool(),
			false,
		)
	} else {
		_, err = db.Exec(
			ctx,
			"SELECT pgaadauth_create_principal($1, $2, $3);",
			data.User.Name.ValueString(),
			data.User.IsAdmin.ValueBool(),
			false,
		)
	}

	if err != nil {
		resp.Diagnostics.AddError("Unable to create Entra ID PostgreSQL user", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *postgresqlEntraIDUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data postgresqlEntraIDUserModel

	if resp.Diagnostics.Append(req.State.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	db, err := r.openDB(ctx, data)
	if err != nil {
		resp.Diagnostics.AddError("Unable to connect to PostgreSQL", err.Error())
		return
	}
	defer db.Close(ctx)

	var exists bool
	if err = db.QueryRow(
		ctx,
		"SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1",
		pgx.Identifier{data.User.Name.ValueString()}.Sanitize(),
	).Scan(&exists); err != nil {
		resp.Diagnostics.AddError("Unable to check PostgreSQL role existence", err.Error())
		return
	}

	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}

	// Get Admin role membership status as this is the only attribute that does not cause a recreation
	var isAdmin bool
	if err = db.QueryRow(
		ctx,
		"SELECT pg_has_role($1, $2, 'member');",
		pgx.Identifier{data.User.Name.ValueString()}.Sanitize(),
		pgx.Identifier{"azure_pg_admin"}.Sanitize(),
	).Scan(&isAdmin); err != nil {
		resp.Diagnostics.AddError("Unable to query PostgreSQL role admin status", err.Error())
		return
	}
	data.User.IsAdmin = types.BoolValue(isAdmin)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *postgresqlEntraIDUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state postgresqlEntraIDUserModel
	var plan postgresqlEntraIDUserModel

	if resp.Diagnostics.Append(req.State.Get(ctx, &state)...); resp.Diagnostics.HasError() {
		return
	}
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...); resp.Diagnostics.HasError() {
		return
	}

	db, err := r.openDB(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to connect to PostgreSQL", err.Error())
		return
	}
	defer db.Close(ctx)

	// Update admin role membership if it has changed
	if state.User.IsAdmin.ValueBool() != plan.User.IsAdmin.ValueBool() {
		query := "REVOKE $1 FROM $2"
		if plan.User.IsAdmin.ValueBool() {
			query = "GRANT $1 TO $2"
		}

		_, err = db.Exec(
			ctx,
			query,
			pgx.Identifier{"azure_pg_admin"}.Sanitize(),
			pgx.Identifier{plan.User.Name.ValueString()}.Sanitize(),
		)

		if err != nil {
			resp.Diagnostics.AddError("Unable to update PostgreSQL admin role membership", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *postgresqlEntraIDUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data postgresqlEntraIDUserModel

	if resp.Diagnostics.Append(req.State.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	db, err := r.openDB(ctx, data)
	if err != nil {
		resp.Diagnostics.AddError("Unable to connect to PostgreSQL", err.Error())
		return
	}
	defer db.Close(ctx)

	_, err = db.Exec(ctx, "DROP ROLE IF EXISTS $1", pgx.Identifier{data.User.Name.ValueString()}.Sanitize())

	if err != nil {
		resp.Diagnostics.AddError("Unable to delete Entra ID PostgreSQL user", err.Error())
		return
	}

	resp.State.RemoveResource(ctx)
}

func (r *postgresqlEntraIDUserResource) openDB(ctx context.Context, data postgresqlEntraIDUserModel) (*pgx.Conn, error) {
	port := int32(5432)
	if !data.Server.Port.IsNull() && !data.Server.Port.IsUnknown() {
		port = data.Server.Port.ValueInt32()
	}

	password := data.Server.AdminPassword.ValueString()
	if password == "" {
		if r.credential == nil {
			return nil, fmt.Errorf("no authentication method available: set server.admin_password or configure provider credentials")
		}

		token, err := r.credential.GetToken(ctx, policy.TokenRequestOptions{
			Scopes: []string{"https://ossrdbms-aad.database.windows.net/.default"},
		})
		if err != nil {
			return nil, err
		}

		password = token.Token
	}

	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=postgres sslmode=require",
		data.Server.Host.ValueString(),
		port,
		data.Server.AdminUsername.ValueString(),
		password,
	)

	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(ctx); err != nil {
		db.Close(ctx)
		return nil, err
	}

	return db, nil
}
