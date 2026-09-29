ephemeral "azutils_token" "source" {
  scopes = ["https://management.azure.com/.default"]
}

resource "azutils_acr_import_image" "example" {
  source_registry  = "sourceregistry.azurecr.io"
  source_image     = "myimage:v1"
  source_password  = ephemeral.azutils_token.source.token
  target_registry  = "destregistry.azurecr.io"
  target_image     = "myimage:v1"
  remove_on_delete = true
}