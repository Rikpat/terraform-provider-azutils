# Terraform Provider Azutils

The goal of this provider is to provide ephemeral tokens to use with azure resources, like databases, using Entra ID authentication, and other utilities missing from azurerm, azuread and azapi providers.

The primary reason was usage in azure pipelines with token federation, but with a possibility to fallback to Azure CLI or other methods in local environment.

For usage with local-exec and other cli tools (like psql) you can instead use these 2 rest calls without needing external providers, but it can't be exported to other terraform resources.
```pwsh
$oidcToken = (Invoke-RestMethod -Method "Post" `
-Uri "$($env:SYSTEM_OIDCREQUESTURI)?serviceConnectionId=$env:ARM_OIDC_AZURE_SERVICE_CONNECTION_ID" `
-ContentType "application/json" `
-Headers @{
Accept = "application/json; api-version=7.1"
Authorization = "Bearer $env:ARM_OIDC_REQUEST_TOKEN"
}).oidcToken

$token = (Invoke-RestMethod -Method "Post" `
-Uri "https://login.microsoftonline.com/$env:ARM_TENANT_ID/oauth2/v2.0/token" `
-ContentType "application/x-www-form-urlencoded" `
-Body @{
grant_type = "client_credentials"
client_id = $env:ARM_CLIENT_ID
client_assertion_type = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
client_assertion = $oidcToken
scope = <scope> # Example: "https://ossrdbms-aad.database.windows.net/.default" for use with mssql or postgres
}).access_token
```

## Using the provider

The provider currently contains:

- Ephemeral resource `azutils_token` for fetching an Entra ID access token.
- Managed resource `azutils_postgresql_entraid_user` for creating and managing Microsoft Entra principals in Azure Database for PostgreSQL.


Main configuration is part of the provider. You can specify credential types and configuration for each credential. It uses credential chain so it will try each credential type in order until it finds one that works. This allows different credentials to be used in different environments while keeping the same resource.


## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) installed on your machine (see [Requirements](#requirements) above).

To compile the provider, run `go install`. This will build the provider and put the provider binary in the `$GOPATH/bin` directory.

To generate or update documentation, run `make generate`.

In order to run the full suite of Acceptance tests, run `make testacc`.

*Note:* Acceptance tests create real resources, and often cost money to run.

```shell
make testacc
```
