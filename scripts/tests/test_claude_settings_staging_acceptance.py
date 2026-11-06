"""The image installer receives the exact scoped settings from its source archive."""

import hashlib
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_source

SETTINGS = "infra/image/claude-settings.json"


class ClaudeSettingsStagingAcceptance(unittest.TestCase):
    def test_real_recipe_archive_carries_settings_bytes_and_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "source.tar"
            with patch.object(sandbox_source, "app_source", return_value=({}, {})), \
                    patch.object(sandbox_source, "commit", return_value="fixture"):
                sandbox_source.source_archive(archive, lock={}, root=ROOT)
            with tarfile.open(archive) as built:
                self.assertIn(SETTINGS, built.getnames())
                self.assertEqual(built.extractfile(SETTINGS).read(), (ROOT / SETTINGS).read_bytes())
                provenance = json.load(built.extractfile("build-source.json"))
                self.assertEqual(provenance["files_sha256"][SETTINGS],
                                 hashlib.sha256((ROOT / SETTINGS).read_bytes()).hexdigest())


if __name__ == "__main__":
    unittest.main()
