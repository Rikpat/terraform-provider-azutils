package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5"
	"github.com/rikpat/terraform-provider-azutils/internal/util"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
	Name        types.String `tfsdk:"name"`
	ObjectID    types.String `tfsdk:"object_id"`
	ObjectType  types.String `tfsdk:"object_type"`
	GlobalRoles types.Set    `tfsdk:"global_roles"`
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

	providerData, ok := req.ProviderData.(*configuredProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *configuredProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.credential = providerData.credential
}

// Schema defines the schema for the resource.
func (r *postgresqlEntraIDUserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Creates a PostgreSQL Role for EntraID user, group or service principal and manages globally granted PostgreSQL roles.",
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
					"global_roles": schema.SetAttribute{
						ElementType:         types.StringType,
						MarkdownDescription: "The global roles assigned to the user. Valid values include `pg_read_all_data`, `azure_pg_admin`, etc. Some PostgreSQL roles (ex. pg_write_all_data) are not supported by Azure PostgreSQL and will fail when assigned.",
						Optional:            true,
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
			false,
			false,
		)
	} else {
		_, err = db.Exec(
			ctx,
			"SELECT pgaadauth_create_principal($1, $2, $3);",
			data.User.Name.ValueString(),
			false,
			false,
		)
	}

	if err != nil {
		resp.Diagnostics.AddError("Unable to create Entra ID PostgreSQL user", err.Error())
		return
	}

	expectedRoles, diags := util.ParseStringSet(data.User.GlobalRoles)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	if err = r.grantGlobalRoles(ctx, db, data.User.Name.ValueString(), expectedRoles); err != nil {
		resp.Diagnostics.AddError("Unable to assign PostgreSQL global roles", err.Error())
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
		"SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)",
		pgx.Identifier{data.User.Name.ValueString()}.Sanitize(),
	).Scan(&exists); err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Unable to check PostgreSQL role existence for user %q", data.User.Name.ValueString()), err.Error())
		return
	}

	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}

	expectedRoles, diags := util.ParseStringSet(data.User.GlobalRoles)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	activeRoles, err := r.checkActiveRoles(ctx, db, data.User.Name.ValueString(), expectedRoles)
	if err != nil {
		resp.Diagnostics.AddError("Unable to check active PostgreSQL roles", err.Error())
		return
	}

	activeRolesSet, diag := activeRoles.ToSetValue()
	resp.Diagnostics.Append(diag...)
	if diag.HasError() {
		return
	}

	data.User.GlobalRoles = activeRolesSet

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

	previousRoles, diags := util.ParseStringSet(state.User.GlobalRoles)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	expectedRoles, diags := util.ParseStringSet(plan.User.GlobalRoles)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	missingRoles, extraRoles := previousRoles.Diff(expectedRoles)

	if err := r.grantGlobalRoles(ctx, db, plan.User.Name.ValueString(), missingRoles); err != nil {
		resp.Diagnostics.AddError("Unable to update PostgreSQL user", fmt.Sprintf("failed to grant roles to user %q: %v", plan.User.Name.ValueString(), err))
	}

	if err := r.revokeGlobalRoles(ctx, db, plan.User.Name.ValueString(), extraRoles); err != nil {
		resp.Diagnostics.AddError("Unable to update PostgreSQL user", fmt.Sprintf("failed to revoke roles from user %q: %v", plan.User.Name.ValueString(), err))
	}

	// If something failed, we need to check which roles were updated and update the state accordingly. We do this by reading the current state of the user again.
	if resp.Diagnostics.HasError() {
		// Needs both previous and expected roles to check which roles are actually active in PostgreSQL.
		activeRoles, err := r.checkActiveRoles(ctx, db, plan.User.Name.ValueString(), previousRoles.Union(expectedRoles))
		if err != nil {
			resp.Diagnostics.AddError("Unable to check active PostgreSQL roles", err.Error())
			return
		}
		activeRolesSet, diag := activeRoles.ToSetValue()
		resp.Diagnostics.Append(diag...)
		if diag.HasError() {
			return
		}
		plan.User.GlobalRoles = activeRolesSet
	}

	// If grant or revoke failed, we still want to update the state to reflect the actual roles in PostgreSQL, so we read the current state again.
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

func (r *postgresqlEntraIDUserResource) grantGlobalRoles(ctx context.Context, db *pgx.Conn, username string, roles *util.StringSet) error {
	query := ""
	for role := range *roles {
		query += fmt.Sprintf("GRANT %s TO %s;\n", pgx.Identifier{role}.Sanitize(), pgx.Identifier{username}.Sanitize())
	}
	_, err := db.Exec(ctx, query)
	return err
}

func (r *postgresqlEntraIDUserResource) revokeGlobalRoles(ctx context.Context, db *pgx.Conn, username string, roles *util.StringSet) error {
	query := ""
	for role := range *roles {
		query += fmt.Sprintf("REVOKE %s FROM %s;\n", pgx.Identifier{role}.Sanitize(), pgx.Identifier{username}.Sanitize())
	}
	_, err := db.Exec(ctx, query)
	return err
}

func (r *postgresqlEntraIDUserResource) checkActiveRoles(ctx context.Context, db *pgx.Conn, username string, roles *util.StringSet) (*util.StringSet, error) {
	activeRoles := make(util.StringSet, 0)
	if len(*roles) > 0 {
		rows, err := db.Query(
			ctx,
			`SELECT configured.role
			FROM unnest($2::text[]) AS configured(role)
			WHERE pg_has_role($1, configured.role, 'member')`,
			username,
			roles.ToList(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query role memberships for user %q: %v", username, err)
		}
		defer rows.Close()

		for rows.Next() {
			var role string
			if err := rows.Scan(&role); err != nil {
				return nil, fmt.Errorf("failed to scan role membership row for user %q: %v", username, err)
			}
			activeRoles.Add(role)
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed while reading role memberships for user %q: %v", username, err)
		}
	}
	return &activeRoles, nil
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
