package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const acrTokenUsername = "00000000-0000-0000-0000-000000000000"

var (
	registryHostPattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*(?::[0-9]+)?$`)
	acrHostPattern       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*\.azurecr\.(io|cn|us)$`)
	acrResourceIDPattern = regexp.MustCompile(`(?i)^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\.ContainerRegistry/registries/[a-zA-Z0-9-]+$`)
	imageRepoPattern     = `[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*`
	sourceImagePattern   = regexp.MustCompile(`^` + imageRepoPattern + `(?::[\w][\w.-]{0,127}|@sha256:[a-f0-9]{64})$`)
	targetImagePattern   = regexp.MustCompile(`^` + imageRepoPattern + `:[\w][\w.-]{0,127}$`)
)

var _ resource.Resource = &acrImportImageResource{}
var _ resource.ResourceWithConfigure = &acrImportImageResource{}

func NewACRImportImageResource() resource.Resource {
	return &acrImportImageResource{}
}

type acrImportImageResource struct {
	providerData *configuredProviderData
}

type targetRegistry struct {
	id          *arm.ResourceID
	loginServer string
	management  *armcontainerregistry.RegistriesClient
	data        *azcontainerregistry.Client
}

type acrImportImageModel struct {
	ID               types.String `tfsdk:"id"`
	SourceRegistry   types.String `tfsdk:"source_registry"`
	SourceImage      types.String `tfsdk:"source_image"`
	SourceUsername   types.String `tfsdk:"source_username"`
	SourcePassword   types.String `tfsdk:"source_password"`
	TargetRegistryID types.String `tfsdk:"target_registry_id"`
	TargetImage      types.String `tfsdk:"target_image"`
	Force            types.Bool   `tfsdk:"force"`
	RemoveOnDelete   types.Bool   `tfsdk:"remove_on_delete"`
	Revision         types.String `tfsdk:"revision"`
}

func (r *acrImportImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_acr_import_image"
}

func (r *acrImportImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Imports one tagged image using the Azure Container Registry server-side import API. If the source registry rate-limits the ACR service or the principal lacks the ARM import action, the provider falls back to pulling and pushing the image locally. The fallback requires network access to both registries and destination push permission. Refresh checks whether the target tag exists and re-imports it if missing. By default, removing this resource from Terraform state does not delete the image; set `remove_on_delete` to delete it during destroy. Requires Terraform 1.11 or later for write-only source passwords.",
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
			"target_registry_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Azure resource ID of the target registry.",
				PlanModifiers:       replace,
				Validators: []validator.String{
					stringvalidator.RegexMatches(acrResourceIDPattern, "must be an Azure Container Registry resource ID"),
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
			"force": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Overwrite the target tag when it already exists. Defaults to `false`.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
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

	providerData, ok := req.ProviderData.(*configuredProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *configuredProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.providerData = providerData
}

func (r *acrImportImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data acrImportImageModel
	if resp.Diagnostics.Append(req.Config.Get(ctx, &data)...); resp.Diagnostics.HasError() {
		return
	}

	target, err := r.targetRegistry(ctx, data.TargetRegistryID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not configure target registry", err.Error())
		return
	}
	parameters := importImageParameters(data)
	if err := r.importImage(ctx, target, parameters); err != nil {
		if !shouldFallbackToLocalCopy(err) {
			resp.Diagnostics.AddError("Could not import image into ACR", err.Error())
			return
		}
		tflog.Warn(ctx, "ACR server-side import unavailable; copying image through the provider", map[string]any{"error": err.Error()})
		if fallbackErr := r.copyImage(ctx, target, parameters); fallbackErr != nil {
			resp.Diagnostics.AddError("Could not import image into ACR", fmt.Sprintf("Server-side import failed: %s; local copy fallback failed: %s", err, fallbackErr))
			return
		}
	}

	data.ID = types.StringValue(data.TargetRegistryID.ValueString() + "/" + data.TargetImage.ValueString())
	data.SourcePassword = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *acrImportImageResource) targetRegistry(ctx context.Context, resourceID string) (*targetRegistry, error) {
	registryID, err := arm.ParseResourceID(resourceID)
	if err != nil {
		return nil, fmt.Errorf("parse resource ID: %w", err)
	}
	clientOptions := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Cloud: r.providerData.cloud}}
	management, err := armcontainerregistry.NewRegistriesClient(registryID.SubscriptionID, r.providerData.credential, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("configure management client: %w", err)
	}
	registry, err := management.Get(ctx, registryID.ResourceGroupName, registryID.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("get registry: %w", err)
	}
	if registry.Properties == nil || registry.Properties.LoginServer == nil || *registry.Properties.LoginServer == "" {
		return nil, fmt.Errorf("registry has no login server")
	}
	loginServer := *registry.Properties.LoginServer
	dataClient, err := azcontainerregistry.NewClient("https://"+loginServer, r.providerData.credential, &azcontainerregistry.ClientOptions{
		ClientOptions: azcore.ClientOptions{Cloud: r.providerData.cloud},
	})
	if err != nil {
		return nil, fmt.Errorf("configure data client: %w", err)
	}
	return &targetRegistry{id: registryID, loginServer: loginServer, management: management, data: dataClient}, nil
}

func (r *acrImportImageResource) importImage(ctx context.Context, target *targetRegistry, parameters armcontainerregistry.ImportImageParameters) error {

	poller, err := target.management.BeginImportImage(ctx, target.id.ResourceGroupName, target.id.Name, parameters, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func importImageParameters(data acrImportImageModel) armcontainerregistry.ImportImageParameters {
	sourceRegistry := data.SourceRegistry.ValueString()
	sourceImage := data.SourceImage.ValueString()
	targetImage := data.TargetImage.ValueString()
	mode := armcontainerregistry.ImportModeNoForce
	if data.Force.ValueBool() {
		mode = armcontainerregistry.ImportModeForce
	}
	parameters := armcontainerregistry.ImportImageParameters{
		Source: &armcontainerregistry.ImportSource{
			RegistryURI: &sourceRegistry,
			SourceImage: &sourceImage,
		},
		TargetTags: []*string{&targetImage},
		Mode:       &mode,
	}
	if !data.SourcePassword.IsNull() && data.SourcePassword.ValueString() != "" {
		password := data.SourcePassword.ValueString()
		parameters.Source.Credentials = &armcontainerregistry.ImportSourceCredentials{Password: &password}
		if !data.SourceUsername.IsNull() {
			username := data.SourceUsername.ValueString()
			parameters.Source.Credentials.Username = &username
		}
	}
	return parameters
}

func shouldFallbackToLocalCopy(err error) bool {
	var responseErr *azcore.ResponseError
	if errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "toomanyrequests") || strings.Contains(message, "too many requests") || strings.Contains(message, "rate limit") || strings.Contains(message, "rate_limit") || strings.Contains(message, "http 429") {
		return true
	}
	return strings.Contains(message, "microsoft.containerregistry/registries/importimage/action") &&
		(strings.Contains(message, "does not have authorization") || errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusForbidden)
}

func (r *acrImportImageResource) copyImage(ctx context.Context, target *targetRegistry, parameters armcontainerregistry.ImportImageParameters) error {
	targetImage := *parameters.TargetTags[0]
	if parameters.Mode == nil || *parameters.Mode != armcontainerregistry.ImportModeForce {
		repository, tag, _ := strings.Cut(targetImage, ":")
		_, err := target.data.GetTagProperties(ctx, repository, tag, nil)
		var responseErr *azcore.ResponseError
		notFound := errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusNotFound
		if err == nil {
			return fmt.Errorf("target tag %q already exists; set force = true to overwrite it", targetImage)
		}
		if !notFound {
			return fmt.Errorf("check target tag: %w", err)
		}
	}

	sourceAuth := authn.Anonymous
	sourceRegistry := *parameters.Source.RegistryURI
	if parameters.Source.Credentials != nil && parameters.Source.Credentials.Password != nil {
		password := *parameters.Source.Credentials.Password
		username := ""
		if parameters.Source.Credentials.Username != nil {
			username = *parameters.Source.Credentials.Username
		}
		if username == "" && acrHostPattern.MatchString(sourceRegistry) {
			sourceAuthenticator, err := r.acrAuthenticator(ctx, sourceRegistry, password)
			if err != nil {
				return fmt.Errorf("authenticate source registry: %w", err)
			}
			sourceAuth = sourceAuthenticator
		} else {
			sourceAuth = &authn.Basic{Username: username, Password: password}
		}
	}

	destinationAuth, err := r.destinationAuth(ctx, target.loginServer)
	if err != nil {
		return fmt.Errorf("authenticate destination registry: %w", err)
	}
	sourceRef, err := name.ParseReference(sourceRegistry + "/" + *parameters.Source.SourceImage)
	if err != nil {
		return fmt.Errorf("parse source image: %w", err)
	}
	targetRef, err := name.ParseReference(target.loginServer + "/" + targetImage)
	if err != nil {
		return fmt.Errorf("parse target image: %w", err)
	}
	return transferImage(ctx, sourceRef, targetRef, sourceAuth, destinationAuth)
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

	target, err := r.targetRegistry(ctx, data.TargetRegistryID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not configure target registry", err.Error())
		return
	}
	repository, tag, _ := strings.Cut(data.TargetImage.ValueString(), ":")
	props, err := target.data.GetTagProperties(ctx, repository, tag, nil)
	if err != nil {
		var responseErr *azcore.ResponseError
		if errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not check target image", err.Error())
		return
	}
	if props.Tag == nil || props.Tag.Digest == nil || *props.Tag.Digest == "" {
		resp.State.RemoveResource(ctx)
	}
}

func (r *acrImportImageResource) destinationAuth(ctx context.Context, loginServer string) (authn.Authenticator, error) {
	scopes := []string{r.providerData.cloud.Services[azcontainerregistry.ServiceName].Audience + "/.default"}
	token, err := r.providerData.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: scopes})
	if err != nil {
		return nil, err
	}
	return r.acrAuthenticator(ctx, loginServer, token.Token)
}

func (r *acrImportImageResource) acrAuthenticator(ctx context.Context, loginServer, aadToken string) (authn.Authenticator, error) {
	client, err := azcontainerregistry.NewAuthenticationClient("https://"+loginServer, &azcontainerregistry.AuthenticationClientOptions{
		ClientOptions: azcore.ClientOptions{Cloud: r.providerData.cloud},
	})
	if err != nil {
		return nil, err
	}
	response, err := client.ExchangeAADAccessTokenForACRRefreshToken(ctx, azcontainerregistry.PostContentSchemaGrantTypeAccessToken, loginServer, &azcontainerregistry.AuthenticationClientExchangeAADAccessTokenForACRRefreshTokenOptions{
		AccessToken: &aadToken,
	})
	if err != nil {
		return nil, err
	}
	if response.RefreshToken == nil || *response.RefreshToken == "" {
		return nil, fmt.Errorf("ACR token exchange returned no refresh token")
	}
	return &authn.Basic{Username: acrTokenUsername, Password: *response.RefreshToken}, nil
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

	target, err := r.targetRegistry(ctx, data.TargetRegistryID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not configure target registry", err.Error())
		return
	}
	repository, tag, _ := strings.Cut(data.TargetImage.ValueString(), ":")
	if _, err := target.data.DeleteTag(ctx, repository, tag, nil); err != nil {
		var responseErr *azcore.ResponseError
		if errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("Could not delete target image", err.Error())
	}
}
