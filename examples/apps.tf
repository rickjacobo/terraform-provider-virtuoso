terraform {
  required_providers {
    virtuoso = {
      source = "rickjacobo/virtuoso"
    }
  }
}

# The apps resources need an admin API key.
provider "virtuoso" {
  endpoint = "https://your-server"
  api_key  = var.virtuoso_api_key
  insecure = true # for a self-signed certificate
}

variable "virtuoso_api_key" {
  type      = string
  sensitive = true
}

# A k3s cluster with two workers. Create waits until the workers have joined.
resource "virtuoso_k8s_cluster" "dev" {
  name         = "dev"
  size         = "large"
  worker_count = 2
  argocd       = true
}

# A VM with Docker and Compose installed.
resource "virtuoso_docker_instance" "tools" {
  name = "tools"
  size = "medium"
}

# A VM whose script needs the tailnet: runs once after Tailscale connects.
resource "virtuoso_vm" "node" {
  name = "node1"
  os   = "ubuntu-24.04"

  post_tailscale_script = <<-EOT
    #!/bin/bash
    echo "joined as $TS_DNSNAME ($TS_IP)" >> /root/joined.txt
  EOT
}

output "kubeconfig" {
  value     = virtuoso_k8s_cluster.dev.kubeconfig
  sensitive = true
}
