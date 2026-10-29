variable "pairing_route" {
  description = "Experimental (#178): a connector container that carries pairing traffic for the secured broker, through two one-way shared volumes. Off by default; needs secured_runtime; never together with peer_connector_probe."
  type        = bool
  default     = false
  nullable    = false
}

variable "pairing_host_port" {
  description = "Experimental (#222): with pairing_route, publish connector's relay on this port of every host address (on macOS, Colima forwards the VM's port to the Mac's addresses). 0 publishes nothing."
  type        = number
  default     = 0
  nullable    = false
  validation {
    condition     = var.pairing_host_port == 0 || (var.pairing_host_port >= 1024 && var.pairing_host_port <= 65535 && floor(var.pairing_host_port) == var.pairing_host_port)
    error_message = "pairing_host_port must be 0 or a whole port from 1024 to 65535."
  }
}

# Enabling, changing or removing the route changes secured's mounts, which
# Incus must not add to or remove from a running instance: refuse unless
# secured is stopped (or not yet created).
resource "terraform_data" "pairing_route_preflight" {
  count            = var.secured_runtime ? 1 : 0
  triggers_replace = [var.incus_socket, var.project_name, var.pairing_route]
  provisioner "local-exec" {
    interpreter = ["python3", "-c"]
    command     = file("${path.module}/secured_preflight.py")
    environment = {
      PAIRTASK_INCUS_SOCKET     = var.incus_socket
      PAIRTASK_INCUS_PROJECT    = var.project_name
      PAIRTASK_PREFLIGHT_ROLES  = "secured,connector"
      PAIRTASK_PREFLIGHT_CHANGE = "enabling, changing or removing pairing_route"
    }
  }
}

# The two socket volumes live as long as secured_runtime, not the route: they
# are attached only while the route is on, so turning it off never deletes a
# volume secured's profile still references in the same apply. They may keep
# stale socket entries; each unit replaces its own when it starts. (The route
# term keeps the precondition below the error for route without secured.)

# Inbound: secured's broker owns the pairing socket here (read-write in
# secured); connector's relay may only connect to it (read-only there).
resource "incus_storage_volume" "pairing_ingress" {
  count        = var.secured_runtime || var.pairing_route ? 1 : 0
  name         = "pairing-ingress"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "16MiB"
    "security.shifted" = "true"
  }
}

# Outbound: connector's egress forwarder owns its socket here (read-write in
# connector); secured's broker may only connect to it (read-only there).
resource "incus_storage_volume" "pairing_egress" {
  count        = var.secured_runtime || var.pairing_route ? 1 : 0
  name         = "pairing-egress"
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
# broker, operator or executor IPC, no status volume, no tools store, no key.
resource "incus_profile" "pairing_connector" {
  count      = var.pairing_route ? 1 : 0
  depends_on = [terraform_data.pairing_route_preflight]
  name       = "pairing-connector"
  project    = incus_project.sandbox.name
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
    name = "pairing-ingress"
    type = "disk"
    properties = {
      source   = incus_storage_volume.pairing_ingress[0].name
      pool     = var.storage_pool
      path     = "/mnt/pairtask-pairing"
      readonly = "true"
    }
  }
  device {
    name = "pairing-egress"
    type = "disk"
    properties = {
      source = incus_storage_volume.pairing_egress[0].name
      pool   = var.storage_pool
      path   = "/mnt/pairtask-egress"
    }
  }
}

resource "incus_instance" "pairing_connector" {
  count     = var.pairing_route ? 1 : 0
  name      = "connector"
  project   = incus_project.sandbox.name
  image     = local.workspace_fingerprint
  type      = "container"
  profiles  = [incus_profile.pairing_connector[0].name]
  ephemeral = false
  running   = var.running
  config = {
    "user.pairtask.managed" = "infra/incus"
    "user.pairtask.role"    = "pairing-connector"
  }

  # A real listening socket on the host (the VM on macOS), not a NAT rule, so
  # Colima's port forwarding sees it; the relay itself listens on 7445.
  dynamic "device" {
    for_each = var.pairing_host_port == 0 ? [] : [var.pairing_host_port]
    content {
      name = "pairing-relay"
      type = "proxy"
      properties = {
        listen  = "tcp:0.0.0.0:${device.value}"
        connect = "tcp:127.0.0.1:7445"
        bind    = "host"
        nat     = "false"
      }
    }
  }

  lifecycle {
    precondition {
      condition     = var.secured_runtime
      error_message = "pairing_route needs secured_runtime: the broker it serves runs in secured."
    }
    precondition {
      condition     = !var.peer_connector_probe
      error_message = "pairing_route and peer_connector_probe can't both be on: each owns the connector."
    }
  }
}
