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

variable "cache_gib" {
  description = "Persistent build caches (Go, npm, pip, uv) for the agents, off the small agent home; mounted only in dev."
  type        = number
  default     = 10
  validation {
    condition     = var.cache_gib >= 1 && var.cache_gib <= 100 && floor(var.cache_gib) == var.cache_gib
    error_message = "cache_gib must be an integer from 1 through 100."
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
  # Build caches (#400) are disposable, so unlike the dev data they may be
  # destroyed: an older checkout that doesn't know the volume removes it, and
  # the image then keeps caches in the home again.
  agent_cache_path = "/var/cache/collab-ai-agent"
  # Where each volume the dev profile mounts goes, the cache included.
  dev_paths = merge({ for key, volume in local.dev_storage : key => volume.path }, { cache = local.agent_cache_path })
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

resource "incus_storage_volume" "agent_cache" {
  name         = "agent-cache"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "${var.cache_gib}GiB"
    "security.shifted" = "false"
  }
}

output "dev_storage" {
  description = "Persistent dev volumes, and the disposable build cache; never mounted into control."
  value = merge(
    { for key, volume in incus_storage_volume.dev_data : key => { volume = volume.name, pool = volume.pool, path = local.dev_storage[key].path } },
    { cache = { volume = incus_storage_volume.agent_cache.name, pool = incus_storage_volume.agent_cache.pool, path = local.agent_cache_path } },
  )
}
