# Requires virtuOSo v0.7.0 or later and an API key with access to the VM.

# Start the VM at 08:00 and stop it at 19:00 on weekdays. A VM has no schedule until you create one.
# Write days as everyday, weekdays, weekends, or lowercase names in week order such as mon,wed,fri.
resource "virtuoso_vm_schedule" "dev" {
  vm       = "dev-box"
  days     = "weekdays"
  start_at = "08:00"
  stop_at  = "19:00"
  tz       = "America/New_York"
}

# Take an automatic snapshot every night at 02:00 and delete each after 7 days.
# Each snapshot is a full copy, so check estimated_bytes before using a long retention.
resource "virtuoso_restore_policy" "dev" {
  vm             = "dev-box"
  at             = "02:00"
  retention_days = 7
  tz             = "America/New_York"
}

# A snapshot Terraform keeps until the resource is destroyed (retention_days defaults to 0).
# Restoring a snapshot is an action, so do it from the web UI, API, CLI or MCP.
resource "virtuoso_snapshot" "before_upgrade" {
  vm   = "dev-box"
  name = "before upgrade"
}

output "restore_policy_space" {
  value = virtuoso_restore_policy.dev.estimated_bytes
}
