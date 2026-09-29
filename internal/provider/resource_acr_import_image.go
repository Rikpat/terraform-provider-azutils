package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const acrTokenUsername = "00000000-0000-0000-0000-000000000000"

var (
	registryHostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*(?::[0-9]+)?$`)
	acrHostPattern      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*\.azurecr\.(io|cn|us)$`)
	imageRepoPattern    = `[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*`
	sourceImagePattern  = regexp.MustCompile(`^` + imageRepoPattern + `(?::[\w][\w.-]{0,127}|@sha256:[a-f0-9]{64})$`)
	targetImagePattern  = regexp.MustCompile(`^` + imageRepoPattern + `:[\w][\w.-]{0,127}$`)
)

var _ resource.Resource = &acrImportImageResource{}
var _ resource.ResourceWithConfigure = &acrImportImageResource{}

func NewACRImportImageResource() resource.Resource {
	return &acrImportImageResource{}
}

type acrImportImageResource struct {
	credential *azidentity.ChainedTokenCredential
}

type acrImportImageModel struct {
	ID                     types.String `tfsdk:"id"`
	SourceRegistry         types.String `tfsdk:"source_registry"`
	SourceImage            types.String `tfsdk:"source_image"`
	SourceUsername         types.String `tfsdk:"source_username"`
	SourcePassword         types.String `tfsdk:"source_password"`
	DestinationRegistryURL types.String `tfsdk:"destination_registry_url"`
	TargetImage            types.String `tfsdk:"target_image"`
	RemoveOnDelete         types.Bool   `tfsdk:"remove_on_delete"`
	Revision               types.String `tfsdk:"revision"`
}

func (r *acrImportImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_acr_import_image"
}

func (r *acrImportImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Copies one tagged image into an Azure Container Registry by pulling and pushing through the Terraform provider. Requires network access to both registries and destination push permission. Refresh checks whether the target tag exists and re-copies it if missing. By default, removing this resource from Terraform state does not delete the image; set `remove_on_delete` to delete it during destroy. Requires Terraform 1.11 or later for write-only source passwords.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"source_registry": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Source registry host, for example `example.azurecr.io` or `docker.io` (without a scheme).",
				PlanModifiers:       replace,
				Validators: []validator.String{
					stringvalidator.RegexMatches(registryHostPattern, "must be a registry hostname without a scheme or path"),
				},
			},
			"source_image": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Source repository and tag or digest, for example `app:v1` or `app@sha256:...`.",
				PlanModifiers:       replace,
				Validators: []validator.String{
					stringvalidator.RegexMatches(sourceImagePattern, "must include a repository and an explicit tag or sha256 digest"),
				},
			},
			"source_username": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional username for the source registry. For an ACR access token, omit this and set `source_password`.",
				PlanModifiers:       replace,
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("source_password")),
					stringvalidator.LengthAtLeast(1),
				},
			},
			"source_password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				WriteOnly:           true,
				MarkdownDescription: "Optional source registry password or access token. Accepts an ephemeral `azutils_token.token`; never stored in state.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"destination_registry_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Destination Azure Container Registry login host without a scheme, for example `example.azurecr.io`.",
				PlanModifiers:       replace,
				Validators: []validator.String{
					stringvalidator.RegexMatches(acrHostPattern, "must be an Azure Container Registry login host without a scheme or path"),
				},
			},
			"target_image": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Destination repository and tag, for example `app:v1`.",
				PlanModifiers:       replace,
				Validators: []validator.String{
					stringvalidator.RegexMatches(targetImagePattern, "must include a repository and an explicit tag"),
				},
			},
			"remove_on_delete": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Delete the target image from the destination registry when this resource is destroyed. Requires destination delete permission. Defaults to `false`.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"revision": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Change this value to re-copy the image when a source tag is updated or credentials rotate.",
				PlanModifiers:       replace,
			},
		},
	}
}

func (r *acrImportImageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	credential, ok := req.ProviderData.(*azidentity.ChainedTokenCredential)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *azidentity.ChainedTokenCredential, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.credential = credential
}

func (r *acrImportImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data acrImportImageModel
	if resp.Diagnostics.Append(req.Config.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	destination := data.DestinationRegistryURL.ValueString()
	if err := r.copyImage(ctx, destination, data); err != nil {
		resp.Diagnostics.AddError("Could not copy image into ACR", err.Error())
		return
	}

	data.ID = types.StringValue(destination + "/" + data.TargetImage.ValueString())
	data.SourcePassword = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *acrImportImageResource) copyImage(ctx context.Context, destination string, data acrImportImageModel) error {
	sourceRef, err := name.ParseReference(data.SourceRegistry.ValueString() + "/" + data.SourceImage.ValueString())
	if err != nil {
		return fmt.Errorf("parse source image: %w", err)
	}
	targetRef, err := name.ParseReference(destination + "/" + data.TargetImage.ValueString())
	if err != nil {
		return fmt.Errorf("parse target image: %w", err)
	}

	sourceAuth := authn.Authenticator(authn.Anonymous)
	if !data.SourcePassword.IsNull() && data.SourcePassword.ValueString() != "" {
		password := data.SourcePassword.ValueString()
		username := data.SourceUsername.ValueString()
		if username == "" && isACRRegistry(data.SourceRegistry.ValueString()) {
			password, err = exchangeACRToken(ctx, http.DefaultClient, "https://"+data.SourceRegistry.ValueString(), password)
			if err != nil {
				return fmt.Errorf("authenticate source registry: %w", err)
			}
			username = acrTokenUsername
		}
		sourceAuth = &authn.Basic{Username: username, Password: password}
	}

	destinationAuth, err := r.destinationAuth(ctx, destination)
	if err != nil {
		return fmt.Errorf("authenticate destination registry: %w", err)
	}
	return transferImage(ctx, sourceRef, targetRef, sourceAuth, destinationAuth)
}

func isACRRegistry(registry string) bool {
	_, err := acrManagementScope(registry)
	return err == nil
}

func exchangeACRToken(ctx context.Context, client *http.Client, registryURL, aadToken string) (string, error) {
	registry, err := url.Parse(registryURL)
	if err != nil || registry.Scheme != "https" || registry.Host == "" {
		return "", fmt.Errorf("invalid ACR URL %q", registryURL)
	}
	form := url.Values{
		"grant_type":   {"access_token"},
		"service":      {registry.Host},
		"access_token": {aadToken},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, registryURL+"/oauth2/exchange", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ACR token exchange returned HTTP %d", response.StatusCode)
	}
	var result struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.RefreshToken == "" {
		return "", fmt.Errorf("ACR token exchange returned no refresh token")
	}
	return result.RefreshToken, nil
}

func acrManagementScope(registry string) (string, error) {
	switch {
	case strings.HasSuffix(registry, ".azurecr.io"):
		return "https://management.azure.com/.default", nil
	case strings.HasSuffix(registry, ".azurecr.cn"):
		return "https://management.chinacloudapi.cn/.default", nil
	case strings.HasSuffix(registry, ".azurecr.us"):
		return "https://management.usgovcloudapi.net/.default", nil
	default:
		return "", fmt.Errorf("unsupported ACR login server %q", registry)
	}
}

func transferImage(ctx context.Context, sourceRef, targetRef name.Reference, sourceAuth, targetAuth authn.Authenticator) error {
	manifest, err := remote.Get(sourceRef, remote.WithContext(ctx), remote.WithAuth(sourceAuth))
	if err != nil {
		return fmt.Errorf("pull source image: %w", err)
	}
	pushOptions := []remote.Option{remote.WithContext(ctx), remote.WithAuth(targetAuth)}

	if manifest.MediaType.IsIndex() {
		index, err := manifest.ImageIndex()
		if err != nil {
			return fmt.Errorf("read source image index: %w", err)
		}
		if err := remote.WriteIndex(targetRef, index, pushOptions...); err != nil {
			return fmt.Errorf("push image index: %w", err)
		}
		return nil
	}
	image, err := manifest.Image()
	if err != nil {
		return fmt.Errorf("read source image: %w", err)
	}
	if err := remote.Write(targetRef, image, pushOptions...); err != nil {
		return fmt.Errorf("push image: %w", err)
	}
	return nil
}

func (r *acrImportImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data acrImportImageModel
	if resp.Diagnostics.Append(req.State.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	loginServer := data.DestinationRegistryURL.ValueString()
	ref, err := name.ParseReference(loginServer + "/" + data.TargetImage.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid target image", err.Error())
		return
	}
	auth, err := r.destinationAuth(ctx, loginServer)
	if err != nil {
		resp.Diagnostics.AddError("Could not authenticate to destination registry", err.Error())
		return
	}
	exists, err := imageExists(ctx, ref, auth)
	if err != nil {
		resp.Diagnostics.AddError("Could not check target image", err.Error())
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
	}
}

func (r *acrImportImageResource) destinationAuth(ctx context.Context, loginServer string) (authn.Authenticator, error) {
	scope, err := acrManagementScope(loginServer)
	if err != nil {
		return nil, err
	}
	token, err := r.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
	if err != nil {
		return nil, err
	}
	refreshToken, err := exchangeACRToken(ctx, http.DefaultClient, "https://"+loginServer, token.Token)
	if err != nil {
		return nil, err
	}
	return &authn.Basic{Username: acrTokenUsername, Password: refreshToken}, nil
}

func imageExists(ctx context.Context, ref name.Reference, auth authn.Authenticator) (bool, error) {
	_, err := remote.Head(ref, remote.WithContext(ctx), remote.WithAuth(auth))
	if err == nil {
		return true, nil
	}
	var registryErr *transport.Error
	if errors.As(err, &registryErr) && registryErr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

func deleteImage(ctx context.Context, ref name.Reference, auth authn.Authenticator) error {
	err := remote.Delete(ref, remote.WithContext(ctx), remote.WithAuth(auth))
	if err == nil {
		return nil
	}
	var registryErr *transport.Error
	if errors.As(err, &registryErr) && registryErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (r *acrImportImageResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "All changes to this import resource require replacement.")
}

func (r *acrImportImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data acrImportImageModel
	if resp.Diagnostics.Append(req.State.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}
	if !data.RemoveOnDelete.ValueBool() {
		return
	}

	loginServer := data.DestinationRegistryURL.ValueString()
	ref, err := name.ParseReference(loginServer + "/" + data.TargetImage.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid target image", err.Error())
		return
	}
	auth, err := r.destinationAuth(ctx, loginServer)
	if err != nil {
		resp.Diagnostics.AddError("Could not authenticate to destination registry", err.Error())
		return
	}
	if err := deleteImage(ctx, ref, auth); err != nil {
		resp.Diagnostics.AddError("Could not delete target image", err.Error())
	}
}
