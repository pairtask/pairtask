provider "incus" {
  default_remote = "sandbox"

  remote {
    name    = "sandbox"
    address = "unix://${var.incus_socket}"
  }

  remote {
    name     = "sandbox-images"
    address  = "https://images.linuxcontainers.org"
    protocol = "simplestreams"
    public   = true
  }
}

resource "incus_project" "sandbox" {
  depends_on = [terraform_data.secured_preflight]

  name          = var.project_name
  description   = "collab-ai dev/control sandbox, managed by infra/incus"
  force_destroy = false

  config = merge({
    "features.images"                 = "true"
    "features.profiles"               = "true"
    "features.networks"               = "false"
    "features.storage.volumes"        = "true"
    "restricted"                      = "true"
    "restricted.containers.privilege" = "isolated"
    "restricted.containers.nesting"   = "allow"
    # Explicit idmap.size is needed for the rootless Docker subordinate IDs.
    # Only the host operator can change instance settings; no Incus API in dev.
    "restricted.containers.lowlevel" = "allow"
    "restricted.devices.disk"        = length(var.host_mounts) > 0 ? "allow" : "managed"
    # Keep this allowlist stable when removing the dev NIC, so Incus can validate
    # the existing profile while Terraform applies the offline opt-out.
    "restricted.devices.nic"     = "managed"
    "restricted.networks.access" = var.dev_network
    # Allowed only while the pairing route is on, for connector's published
    # relay; only the host operator holds the Incus API.
    "restricted.devices.proxy" = var.pairing_route ? "allow" : "block"
    "limits.containers"        = var.secured_runtime ? (var.peer_connector_probe || var.pairing_route ? "3" : "2") : "1"
    "limits.virtual-machines"  = "0"
  }, local.mount_project_config)

  lifecycle {
    precondition {
      condition     = alltrue([for mount in var.host_mounts : mount.container_readonly]) || var.share_identity != null
      error_message = "Writable host mounts require a dedicated share_identity; generate it with sandbox-host.py --share-user."
    }
  }
}

locals {
  workspace_fingerprint = one(concat(incus_image.base[*].fingerprint, incus_image.workspace[*].fingerprint))
}

moved {
  from = incus_image.base
  to   = incus_image.base[0]
}

resource "incus_image" "base" {
  count   = var.image_file == null ? 1 : 0
  project = incus_project.sandbox.name
  source_image = {
    remote       = "sandbox-images"
    name         = var.image_fingerprint
    type         = "container"
    copy_aliases = false
  }
}

resource "incus_image" "workspace" {
  count   = var.image_file == null ? 0 : 1
  project = incus_project.sandbox.name
  source_file = {
    data_path = var.image_file
  }
  lifecycle {
    precondition {
      condition     = var.image_file == null ? true : try(filesha256(var.image_file) == var.image_fingerprint, false)
      error_message = "The workspace image file must exist and match image_fingerprint. Rebuild or restore the original artifact."
    }
    postcondition {
      condition     = self.fingerprint == var.image_fingerprint
      error_message = "Imported image fingerprint differs from the planned artifact; refusing to create the workspace."
    }
  }
}

resource "incus_profile" "sandbox" {
  name    = "offline"
  project = incus_project.sandbox.name

  config = merge({
    "security.privileged"     = "false"
    "security.nesting"        = "true"
    "security.idmap.isolated" = "true"
    "security.idmap.size"     = "131072"
    "security.guestapi"       = "false"
    "boot.autostart"          = "false"
    "limits.cpu"              = tostring(var.cpu_count)
    "limits.memory"           = "${var.memory_gib}GiB"
    # Linux counts threads here too; two agents and their browsers exceed 512.
    "limits.processes" = "2048"
  }, local.mount_profile_config)

  # No proxy or inherited default profile. Control never receives this dev NIC.
  dynamic "device" {
    for_each = var.dev_network_enabled ? [var.dev_network] : []
    content {
      name = "eth0"
      type = "nic"
      properties = {
        name    = "eth0"
        network = device.value
      }
    }
  }

  device {
    name = "root"
    type = "disk"
    properties = {
      path = "/"
      pool = var.storage_pool
      size = "${var.disk_gib}GiB"
    }
  }

  dynamic "device" {
    for_each = incus_storage_volume.dev_data
    content {
      name = device.value.name
      type = "disk"
      properties = {
        source   = device.value.name
        pool     = var.storage_pool
        path     = local.dev_storage[device.key].path
        readonly = "false"
      }
    }
  }

  dynamic "device" {
    for_each = incus_storage_volume.broker_ipc
    content {
      name = "broker-ipc"
      type = "disk"
      properties = {
        source   = device.value.name
        pool     = var.storage_pool
        path     = "/mnt/collab-ipc"
        readonly = "true"
      }
    }
  }

  dynamic "device" {
    for_each = incus_storage_volume.budget_status
    content {
      name = "budget-status"
      type = "disk"
      properties = {
        source   = device.value.name
        pool     = var.storage_pool
        path     = "/mnt/collab-status"
        readonly = "true"
      }
    }
  }

  dynamic "device" {
    for_each = incus_storage_volume.dev_executor
    content {
      name = "dev-executor"
      type = "disk"
      properties = {
        source = device.value.name
        pool   = var.storage_pool
        path   = "/mnt/collab-executor"
      }
    }
  }

  dynamic "device" {
    for_each = var.host_mounts
    content {
      name = "host-${device.value.project_name}"
      type = "disk"
      properties = {
        source   = device.value.host_path
        path     = device.value.container_mount_path
        readonly = tostring(device.value.container_readonly)
        required = "true"
      }
    }
  }
}

resource "incus_instance" "workspace" {
  name      = "workspace"
  project   = incus_project.sandbox.name
  image     = local.workspace_fingerprint
  type      = "container"
  profiles  = [incus_profile.sandbox.name]
  ephemeral = false
  running   = var.running

  config = {
    "user.pairtask.managed" = "infra/incus"
  }
}
