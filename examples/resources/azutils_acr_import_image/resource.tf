ephemeral "azutils_token" "source" {
  scopes = ["https://containerregistry.azure.com/.default"]
}

resource "azutils_acr_import_image" "example" {
  source_registry    = "sourceregistry.azurecr.io"
  source_image       = "myimage:v1"
  source_password    = ephemeral.azutils_token.source.token
  target_registry_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example/providers/Microsoft.ContainerRegistry/registries/destregistry"
  target_image       = "myimage:v1"
  force              = true
  remove_on_delete   = true
}