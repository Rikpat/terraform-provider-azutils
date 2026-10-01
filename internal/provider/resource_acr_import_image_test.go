package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestACRScopes(t *testing.T) {
	audience := cloud.AzurePublic.Services[azcontainerregistry.ServiceName].Audience
	if audience+"/.default" != "https://containerregistry.azure.net/.default" {
		t.Fatalf("ACR audience for AzurePublic = %q", audience)
	}
}

func TestImportImageSchemaValidators(t *testing.T) {
	ctx := context.Background()
	var schemaResponse resource.SchemaResponse
	(&acrImportImageResource{}).Schema(ctx, resource.SchemaRequest{}, &schemaResponse)

	for _, test := range []struct {
		attribute string
		value     string
		valid     bool
	}{
		{"source_registry", "docker.io", true},
		{"source_registry", "registry.example.com:5000", true},
		{"source_registry", "https://docker.io", false},
		{"source_registry", "docker.io/repo", false},
		{"source_image", "team/image:v1", true},
		{"source_image", "team/image@sha256:" + strings.Repeat("a", 64), true},
		{"source_image", "team/image", false},
		{"source_image", "team/image:bad/tag", false},
		{"target_image", "team/image:v1", true},
		{"target_image", "team/image", false},
		{"target_image", "team/image@sha256:" + strings.Repeat("a", 64), false},
		{"target_registry_id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example/providers/Microsoft.ContainerRegistry/registries/example", true},
		{"target_registry_id", "example.azurecr.io", false},
	} {
		t.Run(test.attribute+"="+test.value, func(t *testing.T) {
			attribute, ok := schemaResponse.Schema.Attributes[test.attribute].(schema.StringAttribute)
			if !ok {
				t.Fatalf("attribute %q is not a string attribute", test.attribute)
			}
			var diagnostics validator.StringResponse
			for _, check := range attribute.Validators {
				check.ValidateString(ctx, validator.StringRequest{
					Path:        path.Root(test.attribute),
					ConfigValue: types.StringValue(test.value),
				}, &diagnostics)
			}
			if diagnostics.Diagnostics.HasError() == test.valid {
				t.Fatalf("validation errors for %q: %v, want valid=%t", test.value, diagnostics.Diagnostics, test.valid)
			}
		})
	}
}

func TestParseACRImageImportID(t *testing.T) {
	data, err := parseACRImageImportID("example.azurecr.io/team/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	if data.ID.ValueString() != "example.azurecr.io/team/app:v1" ||
		data.TargetRegistry.ValueString() != "example.azurecr.io" ||
		data.TargetRepository.ValueString() != "team/app" ||
		data.TargetTag.ValueString() != "v1" {
		t.Fatalf("unexpected imported state: %+v", data)
	}
	if !data.SourceRegistry.IsNull() || !data.SourceImage.IsNull() || !data.TargetRegistryID.IsNull() || !data.TargetImage.IsNull() || data.Force.ValueBool() || data.RemoveOnDelete.ValueBool() {
		t.Fatalf("unexpected imported defaults: %+v", data)
	}

	for _, id := range []string{
		"example.azurecr.io/team/app",
		"example.azurecr.io/team/app@sha256:" + strings.Repeat("a", 64),
		"team/app:v1",
		"docker.io/team/app:v1",
	} {
		if _, err := parseACRImageImportID(id); err == nil {
			t.Errorf("parseACRImageImportID(%q) succeeded, want error", id)
		}
	}
}

func TestImportImageParameters(t *testing.T) {
	base := acrImportImageModel{
		SourceRegistry: types.StringValue("source.example.com"),
		SourceImage:    types.StringValue("team/app:v1"),
		SourceUsername: types.StringValue("user"),
		SourcePassword: types.StringValue("password"),
		TargetImage:    types.StringValue("mirror/app:v1"),
	}
	parameters := importImageParameters(base)
	if *parameters.Source.RegistryURI != "source.example.com" || *parameters.Source.SourceImage != "team/app:v1" {
		t.Fatalf("unexpected source: %+v", parameters.Source)
	}
	if len(parameters.TargetTags) != 1 || *parameters.TargetTags[0] != "mirror/app:v1" || *parameters.Mode != armcontainerregistry.ImportModeNoForce {
		t.Fatalf("unexpected target: %+v", parameters)
	}
	if parameters.Source.Credentials == nil || *parameters.Source.Credentials.Username != "user" || *parameters.Source.Credentials.Password != "password" {
		t.Fatalf("unexpected credentials: %+v", parameters.Source.Credentials)
	}

	forced := base
	forced.Force = types.BoolValue(true)
	if mode := importImageParameters(forced).Mode; mode == nil || *mode != armcontainerregistry.ImportModeForce {
		t.Fatalf("force mode = %v, want %s", mode, armcontainerregistry.ImportModeForce)
	}
}

func TestShouldFallbackToLocalCopy(t *testing.T) {
	missingImportPermission := "The client 'example' does not have authorization to perform action 'Microsoft.ContainerRegistry/registries/importImage/action' over scope"
	for _, test := range []struct {
		err  error
		want bool
	}{
		{&azcore.ResponseError{StatusCode: http.StatusTooManyRequests}, true},
		{fmt.Errorf("import: %w", &azcore.ResponseError{StatusCode: http.StatusTooManyRequests}), true},
		{errors.New("TOOMANYREQUESTS: pull rate limit exceeded"), true},
		{errors.New("upstream returned HTTP 429"), true},
		{errors.New(missingImportPermission), true},
		{fmt.Errorf("import: %w", errors.New(missingImportPermission)), true},
		{&azcore.ResponseError{StatusCode: http.StatusForbidden}, false},
		{errors.New("does not have authorization to perform another action"), false},
		{errors.New("image:429 not found"), false},
	} {
		if got := shouldFallbackToLocalCopy(test.err); got != test.want {
			t.Errorf("shouldFallbackToLocalCopy(%v) = %t, want %t", test.err, got, test.want)
		}
	}
}

func TestTransferImage(t *testing.T) {
	registryHandler := registry.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/source/") || strings.HasPrefix(r.URL.Path, "/v2/target/") {
			username, password, ok := r.BasicAuth()
			want := "source-secret"
			if strings.HasPrefix(r.URL.Path, "/v2/target/") {
				want = "target-secret"
			}
			if !ok || username != "user" || password != want {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		registryHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ctx := context.Background()
	sourceAuth := &authn.Basic{Username: "user", Password: "source-secret"}
	targetAuth := &authn.Basic{Username: "user", Password: "target-secret"}

	for _, test := range []struct {
		name  string
		index bool
	}{
		{"single image", false},
		{"multi-platform image", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := name.ParseReference(host+"/source:version", name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			target, err := name.ParseReference(host+"/target:version", name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			if test.index {
				index, err := random.Index(16, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
				if err := remote.WriteIndex(source, index, remote.WithAuth(sourceAuth)); err != nil {
					t.Fatal(err)
				}
			} else {
				image, err := random.Image(16, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := remote.Write(source, image, remote.WithAuth(sourceAuth)); err != nil {
					t.Fatal(err)
				}
			}

			if err := transferImage(ctx, source, target, sourceAuth, targetAuth); err != nil {
				t.Fatal(err)
			}
			sourceDescriptor, err := remote.Get(source, remote.WithAuth(sourceAuth))
			if err != nil {
				t.Fatal(err)
			}
			targetDescriptor, err := remote.Get(target, remote.WithAuth(targetAuth))
			if err != nil {
				t.Fatal(err)
			}
			if sourceDescriptor.Digest != targetDescriptor.Digest || sourceDescriptor.MediaType != targetDescriptor.MediaType {
				t.Fatalf("copy changed manifest: source %s (%s), target %s (%s)", sourceDescriptor.Digest, sourceDescriptor.MediaType, targetDescriptor.Digest, targetDescriptor.MediaType)
			}
		})
	}
}
