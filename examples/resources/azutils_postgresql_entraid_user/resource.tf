resource "azutils_postgresql_entraid_user" "example" {
  server = {
    host           = "my-server.postgres.database.azure.com"
    port           = 5432
    admin_username = "my-admin-upn@mytenant.onmicrosoft.com"
    # Optional: if omitted, provider credentials are used to request
    # https://ossrdbms-aad.database.windows.net/.default token.
    # admin_password = var.postgresql_admin_password
  }

  user = {
    name        = "app-reader"
    object_id   = "11111111-2222-3333-4444-555555555555"
    object_type = "service"
    is_admin    = false
  }
}
