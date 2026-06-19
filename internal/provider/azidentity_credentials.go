package provider

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Select cloud configuration based on the input string, display warning to user if it's not recognized.
func selectCloud(c string) (cloud.Configuration, diag.Diagnostic) {
	switch c {
	case "AzureChina":
		return cloud.AzureChina, nil
	case "AzureGovernment":
		return cloud.AzureGovernment, nil
	case "", "AzurePublic":
		return cloud.AzurePublic, nil
	}
	return cloud.AzurePublic, diag.NewAttributeWarningDiagnostic(path.Root("cloud"), "Invalid cloud value", fmt.Sprintf("The provided cloud value '%s' is not recognized. Falling back to AzurePublic.", c))
}

// Convert from types.String and fetch environment variables if available.
func parseField(in reflect.Value, field reflect.StructField, out reflect.Value, p path.Path) diag.Diagnostic {
	if inVal, ok := in.Interface().(types.String); !ok {
		return diag.NewAttributeErrorDiagnostic(p.AtMapKey(field.Name), "Failed parsing value", "Failed parsing value into string. This is a provider issue, please report it.")
	} else if !inVal.IsNull() {
		out.SetString(inVal.ValueString())
		return nil
	}
	if envs, ok := field.Tag.Lookup("env"); ok {
		for _, env := range strings.Split(envs, ",") {
			if envVal, ok := os.LookupEnv(env); ok {
				out.SetString(envVal)
				return nil
			}
		}
	}
	if missing, ok := field.Tag.Lookup("missing"); ok {
		switch missing {
		case "error":
			return diag.NewAttributeErrorDiagnostic(p.AtMapKey(field.Name), "Missing value", "Missing credential configuration. Could not get value from env or config")
		case "warn":
			return diag.NewAttributeWarningDiagnostic(p.AtMapKey(field.Name), "Missing value", "Missing credential configuration. Could not get value from env or config")
		}
	}
	return nil
}

// Parse object from types.Object to struct of string. Also inject env variables.
func parseObject[M interface{}, P interface{}](ctx context.Context, in types.Object, diags *diag.Diagnostics, p path.Path) *P {
	var model M
	parsed := new(P)
	if !in.IsNull() && !in.IsUnknown() {
		newDiags := in.As(ctx, &model, basetypes.ObjectAsOptions{})
		diags.Append(newDiags...)
		if diags.HasError() {
			return nil
		}
	}
	t := reflect.TypeOf(model)
	v := reflect.ValueOf(model)
	o := reflect.ValueOf(parsed)

	for i := 0; i < t.NumField(); i++ {
		diags.Append(parseField(reflect.Indirect(v).Field(i), t.Field(i), reflect.Indirect(o).Field(i), p))
	}
	return parsed
}

func selectCredentials(ctx context.Context, in *[]types.String, data *AzUtilsProviderModel, clientOptions azcore.ClientOptions) ([]azcore.TokenCredential, diag.Diagnostics) {
	out := make([]azcore.TokenCredential, 0, len(*in))
	diags := diag.Diagnostics{}
	for i, credential := range *in {
		var err error = nil
		var cred azcore.TokenCredential = nil
		c := credential.ValueString()
		p := path.Root(c)
		switch c {
		case "environment_credential":
			cred, err = azidentity.NewEnvironmentCredential(
				&azidentity.EnvironmentCredentialOptions{
					ClientOptions: clientOptions,
				},
			)

		case "managed_identity_credential":
			if props := parseObject[MIcM, MIcP](ctx, data.ManagedIdentityCredential, &diags, p); props != nil {
				cred, err = azidentity.NewManagedIdentityCredential(
					&azidentity.ManagedIdentityCredentialOptions{
						ClientOptions: clientOptions,
						ID:            azidentity.ClientID(props.ClientID),
					})
			} else {
				cred, err = azidentity.NewManagedIdentityCredential(
					&azidentity.ManagedIdentityCredentialOptions{
						ClientOptions: clientOptions,
					})
			}

		case "azure_cli_credential":
			cred, err = azidentity.NewAzureCLICredential(nil)

		case "workload_identity_credential":
			if props := parseObject[WIcM, WIcP](ctx, data.WorkloadIdentityCredential, &diags, p); props != nil {
				cred, err = azidentity.NewWorkloadIdentityCredential(
					// Defaults solved by the SDK (AZURE_CLIENT_ID, AZURE_TENANT_ID)
					&azidentity.WorkloadIdentityCredentialOptions{
						ClientOptions: clientOptions,
						ClientID:      props.ClientID,
						TenantID:      props.TenantID,
					})
			} else {
				cred, err = azidentity.NewWorkloadIdentityCredential(
					// Defaults solved by the SDK (AZURE_CLIENT_ID, AZURE_TENANT_ID)
					&azidentity.WorkloadIdentityCredentialOptions{
						ClientOptions: clientOptions,
					})
			}

		case "azure_pipelines_credential":
			var clientID, tenantID, serviceConnectionID, systemAccessToken string
			if props := parseObject[APcM, APcP](ctx, data.AzurePipelinesCredential, &diags, p); props != nil {
				clientID = props.ClientID
				tenantID = props.TenantID
				serviceConnectionID = props.ServiceConnectionID
				systemAccessToken = props.ServiceConnectionID
			}
			cred, err = azidentity.NewAzurePipelinesCredential(
				tenantID,
				clientID,
				serviceConnectionID,
				systemAccessToken,
				&azidentity.AzurePipelinesCredentialOptions{
					ClientOptions: clientOptions,
				},
			)

		case "client_secret_credential":
			if props := parseObject[CScM, CScP](ctx, data.ClientSecretCredential, &diags, p); props != nil {
				cred, err = azidentity.NewClientSecretCredential(
					props.TenantID,
					props.ClientID,
					props.ClientSecret,
					&azidentity.ClientSecretCredentialOptions{
						ClientOptions: clientOptions,
					},
				)
			} else {
				// Should be caught in validator
				diags.AddAttributeError(p, "Missing configuration", "Missing client_secret_credential configuration. Provide the necessary details or disable credential")
			}

		case "client_certificate_credential":
			if props := parseObject[CCcM, CCcP](ctx, data.ClientCertificateCredential, &diags, p); props != nil {
				certData, err2 := os.ReadFile(props.CertificatePath)
				if err2 != nil {
					diags.AddAttributeError(path.Root(c), "Failed to read certificate file", err2.Error())
					break
				}
				cert, key, err2 := azidentity.ParseCertificates(certData, []byte(props.CertificatePassword))
				if err2 != nil {
					diags.AddAttributeError(p, "Failed to parse certificate file", err2.Error())
					break
				}
				cred, err = azidentity.NewClientCertificateCredential(
					props.TenantID,
					props.ClientID,
					cert,
					key,
					&azidentity.ClientCertificateCredentialOptions{
						ClientOptions: clientOptions,
					},
				)
			} else {
				// Should be caught in validator
				diags.AddAttributeError(path.Root("client_certificate_credential"), "Missing configuration", "Missing client_certificate_credential configuration. Provide the necessary details or disable credential")
			}

		default:
			// Should be caught in validator
			diags.AddAttributeError(path.Root("credentials").AtListIndex(i), "Invalid Credential type", fmt.Sprintf("Unknown type '%s'. Check if you accidentally misspelled the credential type.", c))
		}
		if err != nil {
			diags.AddAttributeWarning(path.Root("credentials").AtListIndex(i), fmt.Sprintf("Error setting up credential '%s'.", c), err.Error())
		} else if cred != nil {
			tflog.Info(ctx, fmt.Sprintf("Appending credential %s", c))
			out = append(out, cred)
		}
	}
	return out, diags
}

func setupCredentialChain(ctx context.Context, data *AzUtilsProviderModel) (*azidentity.ChainedTokenCredential, diag.Diagnostics) {
	// Get credential types to use
	credentialTypes := make([]types.String, 0, len(data.Credentials.Elements()))
	diags := data.Credentials.ElementsAs(ctx, &credentialTypes, false)

	// Get cloud type
	cloud, diag := selectCloud(data.Cloud.ValueString())
	diags.Append(diag)

	credentials, newDiags := selectCredentials(ctx, &credentialTypes, data, azcore.ClientOptions{Cloud: cloud})
	diags.Append(newDiags...)

	cred, err := azidentity.NewChainedTokenCredential(credentials, nil)
	if err != nil {
		diags.AddError("Failed setting up credential chain", err.Error())
	}
	return cred, diags
}
