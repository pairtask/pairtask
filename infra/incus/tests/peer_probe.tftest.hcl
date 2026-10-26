mock_provider "incus" {}

variables {
  incus_socket      = "/tmp/test-incus.sock"
  image_fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  secured_runtime   = true
}

run "normal_deployment_has_no_peer_probe" {
  command = plan
  assert {
    condition = (
      length(incus_instance.connector) == 0 &&
      length(incus_profile.peer_connector) == 0 &&
      length(incus_storage_volume.peer_ipc) == 0 &&
      incus_project.sandbox.config["limits.containers"] == "2" &&
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk" && device.properties.path != "/mnt/collab-peer"])
    )
    error_message = "Normal deployments must retain two containers and no peer IPC mount or probe resources."
  }
}

run "probe_keeps_control_offline_and_agents_out" {
  command = plan
  variables {
    peer_connector_probe = true
    dev_network          = "probe0"
  }
  assert {
    condition = (
      length(incus_instance.connector) == 1 &&
      incus_project.sandbox.config["limits.containers"] == "3" &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "block" &&
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk"]) &&
      output.secured.network == "none" &&
      alltrue([for device in incus_profile.sandbox.device : lookup(device.properties, "path", "") != "/mnt/collab-peer" && lookup(device.properties, "source", "") != "peer-ipc"])
    )
    error_message = "The probe must keep secured without networking and keep the peer IPC volume out of workspace."
  }
  assert {
    condition = (
      length(incus_profile.peer_connector[0].device) == 3 &&
      length([for device in incus_profile.peer_connector[0].device : device if device.type == "disk"]) == 2 &&
      one([for device in incus_profile.peer_connector[0].device : device if device.type == "nic"]).properties.network == "probe0" &&
      one([for device in incus_profile.peer_connector[0].device : device if device.name == "peer-ipc"]).properties.readonly == "true" &&
      one([for device in incus_profile.peer_connector[0].device : device if device.name == "peer-ipc"]).properties.source == incus_storage_volume.peer_ipc[0].name &&
      one([for device in incus_profile.secured[0].device : device if device.name == "peer-ipc"]).properties.source == incus_storage_volume.peer_ipc[0].name &&
      lookup(one([for device in incus_profile.secured[0].device : device if device.name == "peer-ipc"]).properties, "readonly", "false") == "false" &&
      incus_storage_volume.peer_ipc[0].config["security.shifted"] == "true"
    )
    error_message = "Connector must have only root, its managed NIC and read-only shared peer IPC; secured owns the writable IPC mount."
  }
  assert {
    condition = (
      incus_profile.peer_connector[0].config["security.privileged"] == "false" &&
      incus_profile.peer_connector[0].config["security.nesting"] == "false" &&
      incus_profile.peer_connector[0].config["security.idmap.isolated"] == "true" &&
      incus_profile.peer_connector[0].config["security.guestapi"] == "false" &&
      length(incus_instance.connector[0].profiles) == 1 &&
      one(incus_instance.connector[0].profiles) == incus_profile.peer_connector[0].name
    )
    error_message = "Connector must use only its own unprivileged isolated profile, with nesting and guest API disabled."
  }
}

run "probe_requires_secured_runtime" {
  command = plan
  variables {
    peer_connector_probe = true
    secured_runtime      = false
  }
  expect_failures = [incus_instance.connector]
}
