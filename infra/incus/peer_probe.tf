variable "peer_connector_probe" {
  description = "Validation only (#178): add an online connector container and a peer IPC volume shared with secured. Off by default; used by the disposable CI probe, never by an operator sandbox."
  type        = bool
  default     = false
  nullable    = false
}

# The only path between the connector and secured: a small volume holding the
# gate's Unix socket. Secured mounts it read-write; the connector read-only,
# so it can connect but not replace the socket. Workspace never mounts it.
resource "incus_storage_volume" "peer_ipc" {
  count        = var.peer_connector_probe ? 1 : 0
  name         = "peer-ipc"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "16MiB"
    "security.shifted" = "true"
  }
}

# Online, unprivileged and unnested, with its own idmap; no host paths, no
# broker or executor IPC, no status volume, no tools store.
resource "incus_profile" "peer_connector" {
  count   = var.peer_connector_probe ? 1 : 0
  name    = "peer-connector"
  project = incus_project.sandbox.name
  config = {
    "security.privileged"     = "false"
    "security.nesting"        = "false"
    "security.idmap.isolated" = "true"
    "security.guestapi"       = "false"
    "boot.autostart"          = "false"
    "limits.cpu"              = "1"
    "limits.memory"           = "1GiB"
    "limits.processes"        = "256"
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
  device {
    name = "eth0"
    type = "nic"
    properties = {
      name    = "eth0"
      network = var.dev_network
    }
  }
  device {
    name = "peer-ipc"
    type = "disk"
    properties = {
      source   = incus_storage_volume.peer_ipc[0].name
      pool     = var.storage_pool
      path     = "/mnt/collab-peer"
      readonly = "true"
    }
  }
}

resource "incus_instance" "connector" {
  count     = var.peer_connector_probe ? 1 : 0
  name      = "connector"
  project   = incus_project.sandbox.name
  image     = local.workspace_fingerprint
  type      = "container"
  profiles  = [incus_profile.peer_connector[0].name]
  ephemeral = false
  running   = var.running
  config = {
    "user.pairtask.managed" = "infra/incus"
    "user.pairtask.role"    = "peer-connector-probe"
  }

  lifecycle {
    precondition {
      condition     = var.secured_runtime
      error_message = "peer_connector_probe needs secured_runtime: the gate runs in secured."
    }
  }
}
