# Run on the Fleet hub. A VPC made on the hub spans the fleet, and each VM names the node it runs on.
resource "virtuoso_vpc" "cluster" {
  name = "cluster"
  cidr = "10.1.0.0/16"
}

resource "virtuoso_vm" "k3s" {
  for_each = toset(["rack-01", "rack-02"])

  name                  = "k3s-${each.key}"
  node                  = each.key
  vpc                   = virtuoso_vpc.cluster.name
  security_groups       = ["default"]
  size                  = "medium"
  post_tailscale_script = "echo up"
  tags                  = { role = "k3s" }
}

output "k3s_vpc_ips" {
  value = { for k, v in virtuoso_vm.k3s : k => v.vpc_ip }
}
