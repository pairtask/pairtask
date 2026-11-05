"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import json
import subprocess
import unittest
from dataclasses import asdict
from unittest.mock import Mock
from incus_upgrade_fixtures import observation, pins_for, transaction_for, upgrade


class UpgradeReadBoundaryTests(unittest.TestCase):
    def test_transaction_parses_upgrades_dependencies_configures_and_removals(self):
        text = ("Reading package lists...\nInst incus [7.1-1] (7.5-1 Zabbly:stable [amd64])\n"
                "Inst libdependency (1:2.0-3 Zabbly:stable [amd64])\n"
                "Conf incus (7.5-1 Zabbly:stable [amd64])\nRemv legacy [1.0-1]\n")
        runner = Mock()
        runner.vm.return_value = subprocess.CompletedProcess([], 0, text, "")
        actual = upgrade.transaction(runner, {"incus": "7.5-1"})
        self.assertEqual(actual, [("Inst", "incus", "7.5-1"), ("Inst", "libdependency", "1:2.0-3"),
                                  ("Conf", "incus", "7.5-1"), ("Remv", "legacy", "1.0-1")])
        argv, timeout = runner.vm.call_args.args
        self.assertEqual(argv[:3], ["apt-get", "-s", "install"])
        self.assertIn("incus=7.5-1", argv)
        self.assertGreater(timeout, 0)
        self.assertLessEqual(timeout, upgrade.APT)

    def test_debian_epoch_and_package_revision_are_separate_from_server_version(self):
        for package, server in (("7.5-1~ubuntu24.04", "7.5"), ("1:7.0.2-20261105", "7.0.2")):
            with self.subTest(package=package):
                self.assertEqual(upgrade.expected_server(package), server)

    def test_dpkg_comparison_does_not_sort_package_versions_in_python(self):
        runner = Mock()
        runner.vm.return_value = subprocess.CompletedProcess([], 0, "", "")
        self.assertTrue(upgrade.compare(runner, upgrade.Package("7.9-1", "7.10-1", upgrade.ORIGIN)))
        runner.vm.assert_called_once_with(["dpkg", "--compare-versions", "7.10-1", "ge", "7.9-1"], upgrade.QUERY)

    def test_apt_policy_uses_the_candidate_origin_not_an_older_table_entry(self):
        text = ("incus:\n  Installed: 7.1-1\n  Candidate: 7.5-1\n  Version table:\n"
                "     7.5-1 500\n        500 https://pkgs.zabbly.com/incus/stable noble/main amd64 Packages\n"
                " *** 7.1-1 100\n        100 /var/lib/dpkg/status\n")
        package = upgrade.parse_policy(text)["incus"]
        self.assertEqual(asdict(package), {"installed": "7.1-1", "candidate": "7.5-1", "origin": upgrade.ORIGIN})

    def test_observe_reads_actual_all_project_scope_without_mutating_the_vm(self):
        runner = Mock()
        runner.vm.side_effect = self.vm_read
        runner.incus.side_effect = self.incus_read
        obs = upgrade.observe(runner)
        self.assertEqual(obs.server_version, "7.1")
        self.assertEqual(obs.instances[0]["project"], "another-project")
        self.assertEqual(obs.pools[0]["driver"], "dir")
        self.assertFalse(obs.policy_rc_d_exists)
        self.assertEqual(obs.backup_free_bytes, 10**12)
        self.assertTrue(all(value is True for value in obs.upgrades.values()))
        queries = [call.args[0] for call in runner.incus.call_args_list]
        self.assertTrue(any("--all-projects" in argv for argv in queries))
        self.assertTrue(all("colima-pairtask:" in " ".join(argv) for argv in queries))

    def vm_read(self, argv, timeout):
        self.assertGreater(timeout, 0)
        if argv[:2] == ["apt-cache", "policy"]:
            text = "".join(f"{name}:\n  Installed: 7.1-1\n  Candidate: 7.5-1\n  Version table:\n"
                           "     7.5-1 500\n        500 https://pkgs.zabbly.com/incus/stable noble/main amd64 Packages\n"
                           for name in upgrade.PACKAGES)
            return subprocess.CompletedProcess(argv, 0, text, "")
        answers = {"apt-mark": "incus\nzfsutils-linux\n", "systemctl": "active\n",
                   "df": f"Avail\n{10**12}\n", "du": "1024 /var/lib/incus\n", "dpkg": ""}
        if argv == ["test", "-e", upgrade.POLICY]:
            return subprocess.CompletedProcess(argv, 1, "", "")
        if argv[:3] == ["realpath", "-e", "--"]:
            return subprocess.CompletedProcess(argv, 0, argv[-1] + "\n", "")
        self.assertIn(argv[0], answers, f"unexpected VM command in observation: {argv}")
        return subprocess.CompletedProcess(argv, 0, answers[argv[0]], "")

    def incus_read(self, argv, timeout):
        if argv[0] == "query":
            data = {"environment": {"server_version": "7.1"}}
        elif argv[0] == "list":
            data = observation().instances
        else:
            self.assertEqual(argv[:2], ["storage", "list"])
            data = [{"name": "default", "driver": "dir", "config": {"source": "/var/lib/incus/storage-pools/default"}}]
        return subprocess.CompletedProcess(argv, 0, json.dumps(data), "")

    def test_observe_resolves_a_configured_pool_path_before_accepting_it(self):
        runner = Mock()

        def read_with_escape(argv, timeout):
            if argv[:3] == ["realpath", "-e", "--"] and argv[-1].endswith("/storage-pools/default"):
                return subprocess.CompletedProcess(argv, 0, "/external/pool\n", "")
            return self.vm_read(argv, timeout)

        runner.vm.side_effect = read_with_escape
        runner.incus.side_effect = self.incus_read
        obs = upgrade.observe(runner)
        self.assertEqual(obs.pools[0]["resolved"], "/external/pool")
        self.assertTrue(upgrade.blockers(obs, transaction_for(pins_for(obs))))


if __name__ == "__main__":
    unittest.main()
