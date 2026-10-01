ephemeral "azutils_independent_token" "example" {
  credentials = ["azure_cli_credential"]
  scopes      = ["https://management.azure.com/.default"]
}
