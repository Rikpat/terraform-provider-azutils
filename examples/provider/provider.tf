provider "azutils" {
  # Simple pipeline identity with cli fallback for local
  credentials = ["azure_pipelines_credential", "azure_cli_credential"]
}

provider "azutils" {
  alias       = "client2"
  credentials = ["azure_pipelines_credential", "workload_identity_credential"]
  # If running in pipeline agent, but use different service connection as azurerm
  azure_pipelines_credential = {
    service_connection_id = "6174d1c2-f44f-410c-93f1-82a19d400eb8"
    client_id             = "db64e57b-7500-4ece-b682-e8fa8c20d9d5"
    tenant_id             = "6aafbe4c-9457-415e-b57d-834fe4d09c7d"
  }
  # Same identity, if running in AKS cluster
  workload_identity_credential = {
    client_id = "db64e57b-7500-4ece-b682-e8fa8c20d9d5"
    tenant_id = "6aafbe4c-9457-415e-b57d-834fe4d09c7d"
  }
}

variable "client_id" {
  type = string
}

variable "tenant_id" {
  type = string
}

variable "client_secret" {
  type      = string
  sensitive = true
}

provider "azutils" {
  alias       = "identity3"
  credentials = ["client_secret_credential"]
  client_secret_credential = {
    client_id     = var.client_id
    client_secret = var.client_secret
    tenant_id     = var.tenant_id
  }
}

provider "azutils" {
  alias       = "identity4"
  credentials = ["client_certificate_credential"]
  client_certificate_credential = {
    client_id        = var.client_id
    tenant_id        = var.tenant_id
    certificate_path = "./privkey_name.pem"
  }
}
