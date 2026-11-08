terraform {
  required_providers {
    virtuoso = {
      source = "rickjacobo/virtuoso"
    }
  }
}

# An EC2 instance launched through virtuOSo needs an admin API key. The AWS credentials stay on the
# virtuOSo server, in Settings > AWS; Terraform never sees them.
provider "virtuoso" {
  endpoint = "https://your-server"
  api_key  = var.virtuoso_api_key
  insecure = true # for a self-signed certificate
}

variable "virtuoso_api_key" {
  type      = string
  sensitive = true
}

# This costs money while it runs, and running terraform apply is the confirmation. A t3.micro is about
# a cent an hour, plus the disk. Destroying it terminates the instance and removes the security groups made for it.
resource "virtuoso_aws_instance" "web" {
  name          = "web-01"
  instance_type = "t3.micro"
  os            = "ubuntu-24.04"
  ssh_key       = file("~/.ssh/id_ed25519.pub")

  # Optional root disk in GB, 8 to 2000. Left out, the image's own 8 GB is used. The disk is billed while the
  # instance exists (about $0.08 per GB a month) and is deleted with it.
  disk_gb = 40

  # SSH is always open. Open extra ports only to the addresses that need them.
  ports = [
    { port = 443 },
    { port = 8080, source = "203.0.113.7/32" },
  ]
}

output "web_address" {
  value = virtuoso_aws_instance.web.ip
}

output "web_hourly_cost" {
  value = virtuoso_aws_instance.web.estimated_hourly_usd
}
