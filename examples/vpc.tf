terraform {
  required_providers {
    virtuoso = {
      source = "rickjacobo/virtuoso"
    }
  }
}

provider "virtuoso" {
  endpoint = "https://your-server"
  api_key  = var.virtuoso_api_key
  insecure = true
}

variable "virtuoso_api_key" {
  type      = string
  sensitive = true
}

# A private network for the app, with its own address range.
resource "virtuoso_vpc" "app" {
  name = "app"
  cidr = "10.0.0.0/16"
}

# A group that lets the world reach the web servers on port 443.
resource "virtuoso_security_group" "web" {
  vpc         = virtuoso_vpc.app.name
  name        = "web"
  description = "web servers"
}

resource "virtuoso_security_group_rule" "https" {
  vpc            = virtuoso_vpc.app.name
  security_group = virtuoso_security_group.web.name
  direction      = "ingress"
  protocol       = "tcp"
  port_from      = 443
  cidr           = "0.0.0.0/0"
}

# Both VMs get a private address in the VPC and can reach each other by name.
# The web server is also in the web group, and in default, which lets the VPC's
# own VMs talk to each other and lets every VM send anywhere.
resource "virtuoso_vm" "web" {
  name            = "web1"
  size            = "small"
  vpc             = virtuoso_vpc.app.name
  security_groups = ["default", virtuoso_security_group.web.name]
  tags            = { role = "web" }
}

resource "virtuoso_vm" "db" {
  name = "db1"
  size = "small"
  vpc  = virtuoso_vpc.app.name
}

# A public address from the floating IP pool, pointed at the web server.
resource "virtuoso_floating_ip" "web" {
  vpc = virtuoso_vpc.app.name
  vm  = virtuoso_vm.web.name
}

output "web_private_ip" {
  value = virtuoso_vm.web.vpc_ip
}

output "web_public_ip" {
  value = virtuoso_floating_ip.web.address
}

# Peering: a second VPC with its own range, and a peering between the two.
# The ranges must not overlap. A peering opens the way and nothing more: no
# traffic crosses until security group rules in each VPC allow it. Peering is
# not transitive.
resource "virtuoso_vpc" "backend" {
  name = "backend"
  cidr = "10.1.0.0/16"
}

resource "virtuoso_vpc_peering" "app_backend" {
  vpc_a = virtuoso_vpc.app.name
  vpc_b = virtuoso_vpc.backend.name
}

resource "virtuoso_security_group" "api" {
  vpc  = virtuoso_vpc.backend.name
  name = "api"
}

# Rules are directional. This lets app reach the api group on tcp 443 and
# nothing else: the api group has no rule that lets backend start a
# connection into app, and app has no ingress rule naming backend's range.
# To open the other way, add an ingress rule in app that names
# virtuoso_vpc_peering.app_backend.cidr_b, plus an egress rule in backend.
resource "virtuoso_security_group_rule" "api_from_app" {
  vpc            = virtuoso_vpc.backend.name
  security_group = virtuoso_security_group.api.name
  direction      = "ingress"
  protocol       = "tcp"
  port_from      = 443
  cidr           = virtuoso_vpc_peering.app_backend.cidr_a
}

# The sending side needs an egress rule that names the other VPC's range.
resource "virtuoso_security_group_rule" "app_to_api" {
  vpc            = virtuoso_vpc.app.name
  security_group = virtuoso_security_group.web.name
  direction      = "egress"
  protocol       = "tcp"
  port_from      = 443
  cidr           = virtuoso_vpc_peering.app_backend.cidr_b
}
