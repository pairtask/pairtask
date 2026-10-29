mock_provider "incus" {}

variables {
  incus_socket      = "/tmp/test-incus.sock"
  image_fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  secured_runtime   = true
}

run "legacy_route_does_not_start_publishing" {
  command = plan
  variables {
    pairing_route = true
  }
  assert {
    condition = (
      length(incus_instance.pairing_connector[0].device) == 0 &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "allow"
    )
    error_message = "A legacy route permits connector proxies but must not create a listener without an explicit port."
  }
}

run "host_port_without_route_is_inert" {
  command = plan
  variables {
    pairing_host_port = 8445
  }
  assert {
    condition = (
      length(incus_instance.pairing_connector) == 0 &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "block"
    )
    error_message = "Saved publishing intent must not keep the connector or proxy permission when pairing is off."
  }
}

run "only_connector_publishes_relay" {
  command = plan
  variables {
    pairing_route     = true
    pairing_host_port = 8445
  }
  assert {
    condition = (
      length(incus_instance.pairing_connector[0].device) == 1 &&
      one(incus_instance.pairing_connector[0].device).name == "pairing-relay" &&
      one(incus_instance.pairing_connector[0].device).type == "proxy" &&
      one(incus_instance.pairing_connector[0].device).properties == tomap({
        listen  = "tcp:0.0.0.0:8445"
        connect = "tcp:127.0.0.1:7445"
        bind    = "host"
        nat     = "false"
      }) &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "allow"
    )
    error_message = "Publishing must create exactly the real host TCP listener to the connector relay, without NAT."
  }
  assert {
    condition = (
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk"]) &&
      alltrue([for device in incus_profile.sandbox.device : device.type != "proxy"]) &&
      alltrue([for device in incus_profile.pairing_connector[0].device : device.type != "proxy"]) &&
      alltrue([for device in incus_instance.workspace.device : device.type != "proxy"]) &&
      alltrue([for device in incus_instance.secured[0].device : device.type == "disk"]) &&
      output.secured.network == "none"
    )
    error_message = "Publishing must not add network devices to secured or a proxy to any shared profile."
  }
}

run "explicit_zero_keeps_route_private" {
  command = plan
  variables {
    pairing_route     = true
    pairing_host_port = 0
  }
  assert {
    condition     = length(incus_instance.pairing_connector[0].device) == 0
    error_message = "An explicit zero must remove the listener while retaining the route."
  }
}

run "reject_negative_port" {
  command = plan
  variables {
    pairing_host_port = -1
  }
  expect_failures = [var.pairing_host_port]
}

run "reject_privileged_port" {
  command = plan
  variables {
    pairing_host_port = 1023
  }
  expect_failures = [var.pairing_host_port]
}

run "reject_overflow_port" {
  command = plan
  variables {
    pairing_host_port = 65536
  }
  expect_failures = [var.pairing_host_port]
}

run "reject_fractional_port" {
  command = plan
  variables {
    pairing_host_port = 7445.5
  }
  expect_failures = [var.pairing_host_port]
}
