mock_provider "incus" {}

variables {
  incus_socket      = "/tmp/test-incus.sock"
  image_fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  secured_runtime   = true
}

run "route_is_detached_by_default" {
  command = plan
  assert {
    condition = (
      length(incus_instance.pairing_connector) == 0 &&
      length(incus_profile.pairing_connector) == 0 &&
      length(incus_storage_volume.pairing_ingress) == 1 &&
      length(incus_storage_volume.pairing_egress) == 1 &&
      incus_project.sandbox.config["limits.containers"] == "2" &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "block" &&
      alltrue([for profile in [incus_profile.secured[0], incus_profile.sandbox] : alltrue([
        for device in profile.device : (
          !contains(["/mnt/pairtask-pairing", "/mnt/pairtask-egress"], lookup(device.properties, "path", "")) &&
          !contains([incus_storage_volume.pairing_ingress[0].name, incus_storage_volume.pairing_egress[0].name], lookup(device.properties, "source", ""))
        )
      ])])
    )
    error_message = "With pairing off, retain both secured socket volumes but attach neither; no connector or proxy permission may remain."
  }
}

run "route_preserves_secured_and_workspace_boundaries" {
  command = plan
  variables {
    pairing_route = true
    dev_network   = "route0"
  }
  assert {
    condition = (
      length(incus_instance.pairing_connector) == 1 &&
      incus_instance.pairing_connector[0].name == "connector" &&
      incus_instance.pairing_connector[0].config["user.pairtask.role"] == "pairing-connector" &&
      incus_project.sandbox.config["limits.containers"] == "3" &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "allow" &&
      length(incus_instance.connector) == 0 &&
      length(incus_storage_volume.peer_ipc) == 0 &&
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk"]) &&
      output.secured.network == "none"
    )
    error_message = "The installed route must keep secured offline and remain separate from the old probe."
  }
  assert {
    condition = alltrue([for device in incus_profile.sandbox.device : (
      !contains(["/mnt/pairtask-pairing", "/mnt/pairtask-egress"], lookup(device.properties, "path", "")) &&
      !contains([incus_storage_volume.pairing_ingress[0].name, incus_storage_volume.pairing_egress[0].name], lookup(device.properties, "source", ""))
    )])
    error_message = "Workspace must receive neither route path nor either shared volume under an alias."
  }
  assert {
    condition = (
      length(incus_profile.pairing_connector[0].device) == 4 &&
      length([for device in incus_profile.pairing_connector[0].device : device if device.type == "disk"]) == 3 &&
      one([for device in incus_profile.pairing_connector[0].device : device if device.type == "nic"]).properties.network == "route0" &&
      one([for device in incus_profile.pairing_connector[0].device : device if device.name == "root"]).properties.path == "/" &&
      incus_profile.pairing_connector[0].config["security.privileged"] == "false" &&
      incus_profile.pairing_connector[0].config["security.nesting"] == "false" &&
      incus_profile.pairing_connector[0].config["security.idmap.isolated"] == "true" &&
      incus_profile.pairing_connector[0].config["security.guestapi"] == "false" &&
      length(incus_instance.pairing_connector[0].profiles) == 1 &&
      one(incus_instance.pairing_connector[0].profiles) == incus_profile.pairing_connector[0].name
    )
    error_message = "Connector must have only root, one managed NIC and two route volumes in its isolated unprivileged profile."
  }
  assert {
    condition = (
      one([for device in incus_profile.pairing_connector[0].device : device if device.name == "pairing-ingress"]).properties.readonly == "true" &&
      one([for device in incus_profile.secured[0].device : device if device.name == "pairing-egress"]).properties.readonly == "true" &&
      lookup(one([for device in incus_profile.secured[0].device : device if device.name == "pairing-ingress"]).properties, "readonly", "false") == "false" &&
      lookup(one([for device in incus_profile.pairing_connector[0].device : device if device.name == "pairing-egress"]).properties, "readonly", "false") == "false" &&
      alltrue([for profile in [incus_profile.secured[0], incus_profile.pairing_connector[0]] : (
        one([for device in profile.device : device if device.name == "pairing-ingress"]).properties.source == incus_storage_volume.pairing_ingress[0].name &&
        one([for device in profile.device : device if device.name == "pairing-egress"]).properties.source == incus_storage_volume.pairing_egress[0].name &&
        one([for device in profile.device : device if device.name == "pairing-ingress"]).properties.path == "/mnt/pairtask-pairing" &&
        one([for device in profile.device : device if device.name == "pairing-egress"]).properties.path == "/mnt/pairtask-egress"
      )]) &&
      incus_storage_volume.pairing_ingress[0].config["security.shifted"] == "true" &&
      incus_storage_volume.pairing_egress[0].config["security.shifted"] == "true" &&
      incus_storage_volume.pairing_ingress[0].config["size"] == "16MiB" &&
      incus_storage_volume.pairing_egress[0].config["size"] == "16MiB" &&
      incus_storage_volume.pairing_ingress[0].name != incus_storage_volume.pairing_egress[0].name
    )
    error_message = "The two distinct shifted volumes must be writable only by the socket owner on each side."
  }
}

run "route_requires_secured_runtime" {
  command = plan
  variables {
    pairing_route   = true
    secured_runtime = false
  }
  expect_failures = [incus_instance.pairing_connector]
}

run "route_and_old_probe_are_mutually_exclusive" {
  command = plan
  variables {
    pairing_route        = true
    peer_connector_probe = true
  }
  expect_failures = [incus_instance.pairing_connector]
}

run "no_socket_volumes_without_secured_runtime" {
  command = plan
  variables {
    secured_runtime = false
    pairing_route   = false
  }
  assert {
    condition = (
      length(incus_storage_volume.pairing_ingress) == 0 &&
      length(incus_storage_volume.pairing_egress) == 0 &&
      length(incus_instance.pairing_connector) == 0 &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "block"
    )
    error_message = "A deployment without secured runtime needs neither pairing socket volumes nor a route."
  }
}
