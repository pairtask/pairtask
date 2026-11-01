mock_provider "incus" {}

variables {
  incus_socket      = "/tmp/test-incus.sock"
  image_fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  secured_runtime   = true
}

run "name_file_only_while_route_enabled" {
  command = plan
  variables {
    pairing_route = false
    pairing_name  = "Saved name"
  }
  assert {
    condition     = length(incus_instance.secured[0].file) == 0
    error_message = "A saved name must not provision a name file while pairing is off."
  }
}

run "provider_file_is_json_root_owned_and_guest_api_stays_off" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = "  \"quoted\" \\ $cash % #tag;  "
  }
  assert {
    condition = (
      length(incus_instance.secured[0].file) == 1 &&
      one(incus_instance.secured[0].file).target_path == "/etc/pairtask/pairing-name.json" &&
      jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name &&
      one(incus_instance.secured[0].file).uid == 0 &&
      one(incus_instance.secured[0].file).gid == 0 &&
      one(incus_instance.secured[0].file).mode == "0644" &&
      incus_profile.secured[0].config["security.guestapi"] == "false"
    )
    error_message = "Provision the exact JSON name as root:root 0644 without enabling the guest API."
  }
}

run "vector_empty" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[0].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_ascii" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[1].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_edge_spaces" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[2].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_space_only" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[3].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_ascii_64" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[4].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_ascii_65" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[5].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_utf8_64" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[6].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_utf8_65" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[7].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_emoji_64" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[8].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_emoji_68" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[9].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_combining_mark" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[10].name
  }
  assert {
    condition     = jsondecode(one(incus_instance.secured[0].file).content).name == var.pairing_name
    error_message = "A valid preliminary name must reach the provider file unchanged."
  }
}

run "vector_tab" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[11].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_line_feed" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[12].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_carriage_return" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[13].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_nul" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[14].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_escape" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[15].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_delete" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[16].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_line_separator" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[17].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_paragraph_separator" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[18].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_nonbreaking_space" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[19].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_ideographic_space" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[20].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_zero_width_joiner" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[21].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_bom" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[22].name
  }
  expect_failures = [var.pairing_name]
}

run "vector_unassigned" {
  command = plan
  variables {
    pairing_route = true
    pairing_name  = jsondecode(file("../../testdata/pairing_name_vectors.json"))[23].name
  }
  expect_failures = [var.pairing_name]
}
