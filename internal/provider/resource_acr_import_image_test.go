package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		{"target_registry", "example.azurecr.io", true},
		{"target_registry", "https://example.azurecr.io", false},
		{"target_registry", "https://example.azurecr.io/repo", false},
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

func TestImageExists(t *testing.T) {
	registryHandler := registry.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/denied/"):
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case strings.HasPrefix(r.URL.Path, "/v2/broken/"):
			http.Error(w, "registry unavailable", http.StatusInternalServerError)
		default:
			registryHandler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ctx := context.Background()
	ref := func(repository string) name.Reference {
		t.Helper()
		parsed, err := name.ParseReference(host+"/"+repository+":v1", name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	image, err := random.Image(16, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref("present"), image); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		repository string
		wantExists bool
		wantError  bool
	}{
		{"present", true, false},
		{"missing", false, false},
		{"denied", false, true},
		{"broken", false, true},
	} {
		t.Run(test.repository, func(t *testing.T) {
			exists, err := imageExists(ctx, ref(test.repository), authn.Anonymous)
			if exists != test.wantExists || (err != nil) != test.wantError {
				t.Fatalf("imageExists(%s) = (%t, %v), want exists=%t error=%t", test.repository, exists, err, test.wantExists, test.wantError)
			}
		})
	}
}

func TestDeleteImage(t *testing.T) {
	registryHandler := registry.New()
	var deletedReference string
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deleted && r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/manifests/v1") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			deletedReference = r.URL.Path
			if strings.HasSuffix(r.URL.Path, ":v1") {
				http.Error(w, "UNSUPPORTED: A manifest can only be deleted by digest.", http.StatusMethodNotAllowed)
				return
			}
			deleted = true
		}
		registryHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref, err := name.ParseReference(host+"/delete:v1", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	image, err := random.Image(16, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, image); err != nil {
		t.Fatal(err)
	}

	if err := deleteImage(context.Background(), ref, authn.Anonymous); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deletedReference, "/manifests/sha256:") {
		t.Fatalf("deleted reference %q is not a digest", deletedReference)
	}
	if exists, err := imageExists(context.Background(), ref, authn.Anonymous); err != nil || exists {
		t.Fatalf("image exists after deletion: exists=%t err=%v", exists, err)
	}
	if err := deleteImage(context.Background(), ref, authn.Anonymous); err != nil {
		t.Fatalf("deleting an absent image: %v", err)
	}
}

func TestExchangeACRToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/exchange" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "access_token" || r.Form.Get("service") != r.Host || r.Form.Get("access_token") != "entra-token" {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"refresh_token":"acr-token"}`)
	}))
	defer server.Close()
	token, err := exchangeACRToken(context.Background(), server.Client(), server.URL, "entra-token")
	if err != nil || token != "acr-token" {
		t.Fatalf("exchangeACRToken() = (%q, %v), want acr-token", token, err)
	}
}
