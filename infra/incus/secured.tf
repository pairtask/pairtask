variable "secured_runtime" {
  description = "Add offline control services with one shared broker, private state, dev executor and status IPC."
  type        = bool
  default     = false
  nullable    = false
}

# Check both runtimes when enabling/upgrading the IPC layout, before Incus changes.
# Keep both stopped through a failed/retried apply to avoid hot-added read-only mounts.
resource "terraform_data" "secured_preflight" {
  count            = var.secured_runtime ? 1 : 0
  triggers_replace = [var.incus_socket, var.project_name, "broker-ipc-v1"]
  provisioner "local-exec" {
    interpreter = ["python3", "-c"]
    command     = file("${path.module}/secured_preflight.py")
    environment = {
      PAIRTASK_INCUS_SOCKET  = var.incus_socket
      PAIRTASK_INCUS_PROJECT = var.project_name
    }
  }
}

resource "incus_storage_volume" "secured_state" {
  count        = var.secured_runtime ? 1 : 0
  name         = "secured-state"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "1GiB"
    "security.shifted" = "true"
  }
}

variable "secured_tools_gib" {
  description = "Persistent, root-owned tool store for secured; never shared with the workspace."
  type        = number
  default     = 2
  validation {
    condition     = var.secured_tools_gib >= 1 && var.secured_tools_gib <= 100 && floor(var.secured_tools_gib) == var.secured_tools_gib
    error_message = "secured_tools_gib must be an integer from 1 through 100."
  }
}

resource "incus_storage_volume" "secured_tools" {
  count        = var.secured_runtime ? 1 : 0
  name         = "secured-tools"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "${var.secured_tools_gib}GiB"
    "security.shifted" = "true"
  }
  lifecycle {
    prevent_destroy = true
  }
}

resource "incus_storage_volume" "budget_status" {
  count        = var.secured_runtime ? 1 : 0
  name         = "budget-status"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "16MiB"
    "security.shifted" = "true"
  }
}

resource "incus_profile" "secured" {
  count      = var.secured_runtime ? 1 : 0
  depends_on = [terraform_data.pairing_route_preflight]
  name       = "secured-offline"
  project    = incus_project.sandbox.name
  config = {
    "security.privileged"     = "false"
    "security.nesting"        = "false"
    "security.idmap.isolated" = "true"
    "security.guestapi"       = "false"
    "boot.autostart"          = "false"
    "limits.cpu"              = "1"
    "limits.memory"           = "2GiB"
    "limits.processes"        = "256"
  }

  # No host paths, raw.idmap, NIC, proxy, or inherited workspace/default profile.
  device {
    name = "root"
    type = "disk"
    properties = {
      path = "/"
      pool = var.storage_pool
      size = "${var.disk_gib}GiB"
    }
  }
  device {
    name = "secured-state"
    type = "disk"
    properties = {
      source = incus_storage_volume.secured_state[0].name
      pool   = var.storage_pool
      path   = "/var/lib/collab-ai-secured"
    }
  }
  device {
    name = "broker-ipc"
    type = "disk"
    properties = {
      source = incus_storage_volume.broker_ipc[0].name
      pool   = var.storage_pool
      path   = "/mnt/collab-ipc"
    }
  }
  device {
    name = "dev-executor"
    type = "disk"
    properties = {
      source   = incus_storage_volume.dev_executor[0].name
      pool     = var.storage_pool
      path     = "/mnt/collab-executor"
      readonly = "true"
    }
  }
  device {
    name = "budget-status"
    type = "disk"
    properties = {
      source = incus_storage_volume.budget_status[0].name
      pool   = var.storage_pool
      path   = "/mnt/collab-status"
    }
  }
  device {
    name = "secured-tools"
    type = "disk"
    properties = {
      source = incus_storage_volume.secured_tools[0].name
      pool   = var.storage_pool
      path   = "/var/lib/collab-ai-tools"
    }
  }
  # The pairing route (#178): the broker's pairing socket volume read-write,
  # connector's egress socket volume read-only.
  dynamic "device" {
    for_each = var.pairing_route ? [incus_storage_volume.pairing_ingress[0].name] : []
    content {
      name = "pairing-ingress"
      type = "disk"
      properties = {
        source = device.value
        pool   = var.storage_pool
        path   = "/mnt/pairtask-pairing"
      }
    }
  }
  dynamic "device" {
    for_each = var.pairing_route ? [incus_storage_volume.pairing_egress[0].name] : []
    content {
      name = "pairing-egress"
      type = "disk"
      properties = {
        source   = device.value
        pool     = var.storage_pool
        path     = "/mnt/pairtask-egress"
        readonly = "true"
      }
    }
  }
  # Validation only (#178): the peer gate's socket volume, read-write here.
  dynamic "device" {
    for_each = var.peer_connector_probe ? [incus_storage_volume.peer_ipc[0].name] : []
    content {
      name = "peer-ipc"
      type = "disk"
      properties = {
        source = device.value
        pool   = var.storage_pool
        path   = "/mnt/collab-peer"
      }
    }
  }
}

resource "incus_instance" "secured" {
  count     = var.secured_runtime ? 1 : 0
  name      = "secured"
  project   = incus_project.sandbox.name
  image     = local.workspace_fingerprint
  type      = "container"
  profiles  = [incus_profile.secured[0].name]
  ephemeral = false
  running   = var.running
  config = {
    "user.pairtask.managed" = "infra/incus"
    "user.pairtask.role"    = "secured"
  }
}

output "secured" {
  description = "Host-only administration; provisioning does not start authenticated clients or a budget supervisor."
  value = var.secured_runtime ? {
    project         = incus_project.sandbox.name
    instance        = incus_instance.secured[0].name
    budget_dir      = "/var/lib/collab-ai-secured/budgets"
    status_dir      = "/mnt/collab-status"
    network         = "none"
    state_volume    = incus_storage_volume.secured_state[0].name
    status_volume   = incus_storage_volume.budget_status[0].name
    executor_volume = incus_storage_volume.dev_executor[0].name
    broker_socket   = "/mnt/collab-ipc/broker.sock"
    broker_volume   = incus_storage_volume.broker_ipc[0].name
    broker_database = "/var/lib/collab-ai-secured/broker/broker.db"
  } : null
}
