terraform {
  required_providers {
    virtuoso = {
      source = "rickjacobo/virtuoso"
    }
  }
}

provider "virtuoso" {
  endpoint = "https://192.168.33.99"
  api_key  = var.virtuoso_api_key
  insecure = true
}

variable "virtuoso_api_key" {
  type      = string
  sensitive = true
}

resource "virtuoso_ssh_key" "deploy" {
  name       = "deploy-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "virtuoso_vm" "web" {
  name      = "web-server"
  size      = "medium"
  os        = "ubuntu-24.04"
  disk_gb   = 40
  ssh_key   = virtuoso_ssh_key.deploy.public_key
  started   = true
  autostart = true
}

output "web_ip" {
  value = virtuoso_vm.web.ip
}

output "web_password" {
  value     = virtuoso_vm.web.password
  sensitive = true
}
