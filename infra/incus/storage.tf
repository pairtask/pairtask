variable "workspace_gib" {
  description = "Persistent dev project storage, independent of the replaceable root disk."
  type        = number
  default     = 10
  validation {
    condition     = var.workspace_gib >= 1 && var.workspace_gib <= 100 && floor(var.workspace_gib) == var.workspace_gib
    error_message = "workspace_gib must be an integer from 1 through 100."
  }
}

variable "agent_home_gib" {
  description = "Persistent agent settings, memory and session files; mounted only in dev."
  type        = number
  default     = 2
  validation {
    condition     = var.agent_home_gib >= 1 && var.agent_home_gib <= 100 && floor(var.agent_home_gib) == var.agent_home_gib
    error_message = "agent_home_gib must be an integer from 1 through 100."
  }
}

variable "docker_gib" {
  description = "Persistent rootless Docker images, containers and volumes; mounted only in dev."
  type        = number
  default     = 10
  validation {
    condition     = var.docker_gib >= 1 && var.docker_gib <= 100 && floor(var.docker_gib) == var.docker_gib
    error_message = "docker_gib must be an integer from 1 through 100."
  }
}

variable "tools_gib" {
  description = "Persistent, root-owned tool store for the workspace; agents read it, only the host installs into it."
  type        = number
  default     = 4
  validation {
    condition     = var.tools_gib >= 1 && var.tools_gib <= 100 && floor(var.tools_gib) == var.tools_gib
    error_message = "tools_gib must be an integer from 1 through 100."
  }
}

locals {
  dev_storage = {
    workspace = { name = "workspace-data", path = "/workspace", size = var.workspace_gib }
    home      = { name = "agent-home", path = "/home/agent", size = var.agent_home_gib }
    docker    = { name = "docker-data", path = "/var/lib/collab-ai-docker", size = var.docker_gib }
    tools     = { name = "workspace-tools", path = "/var/lib/collab-ai-tools", size = var.tools_gib }
  }
}

resource "incus_storage_volume" "dev_data" {
  for_each     = local.dev_storage
  name         = each.value.name
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size" = "${each.value.size}GiB"
    # Private to one dev instance. Incus remaps ownership on replacement.
    # Dynamic shared-volume mounts can hide ordinary nested host mounts.
    "security.shifted" = "false"
  }
  lifecycle {
    # Deliberate deletion requires changing this literal after backup/review.
    prevent_destroy = true
  }
}

output "dev_storage" {
  description = "Persistent dev volumes; never mounted into control."
  value = { for key, volume in incus_storage_volume.dev_data : key => {
    volume = volume.name
    pool   = volume.pool
    path   = local.dev_storage[key].path
  } }
}
