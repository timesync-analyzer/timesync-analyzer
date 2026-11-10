"""Run with python3 -m unittest discover -s scripts -p 'test_offline_bundle.py'.

Exercises bundle assembly and validation without building images or using a daemon.
The release still needs the offline runtime acceptance checks in the deployment guide.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[1]


class OfflineBundleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        fake = self.root / "bin"
        fake.mkdir()
        docker = fake / "docker"
        docker.write_text("""#!/bin/sh
set -eu
printf '%s\\n' "$*" >> "$DOCKER_TEST_LOG"
case "$1" in
  info|buildx|pull|tag) ;;
  image) printf '[]\\n' ;;
  save) printf 'fake saved images' > "$3" ;;
  run) printf '{"info":{"version":"test"}}\\n' ;;
  *) exit 1 ;;
esac
""")
        docker.chmod(0o755)
        self.log = self.root / "docker.log"
        self.env = dict(os.environ, PATH=f"{fake}:{os.environ['PATH']}",
                        DOCKER_TEST_LOG=str(self.log))

    def build(self, release="offline-test", output=None, platform="linux/amd64"):
        return subprocess.run(
            ["sh", str(REPO / "scripts/build-offline.sh"), release,
             str(output or self.root / "bundle"), platform],
            env=self.env, capture_output=True, text=True)

    def test_bundle_has_images_configs_and_valid_checksums(self):
        output = self.root / "bundle with spaces"
        result = self.build(output=output)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("RELEASE=offline-test", (output / ".env.example").read_text())
        self.assertFalse((output / ".env").exists())
        for line in (output / "SHA256SUMS").read_text().splitlines():
            digest, name = line.split(maxsplit=1)
            self.assertEqual(digest, hashlib.sha256((output / name).read_bytes()).hexdigest())
        calls = self.log.read_text()
        for name in ("analyzer", "grafana", "db", "renderer"):
            self.assertIn(f"timesync-{name}:offline-test", calls)
        self.assertIn("--platform linux/amd64", calls)

    def test_rejects_invalid_inputs_before_calling_docker(self):
        for release in ("../escape", "tag with spaces", "-option", "a" * 129):
            self.assertNotEqual(self.build(release=release).returncode, 0)
        self.assertNotEqual(self.build(platform="linux/unknown").returncode, 0)
        self.assertNotEqual(self.build(output=self.root).returncode, 0)
        self.assertFalse(self.log.exists())

    def test_failed_docker_stops_bundle_creation(self):
        (self.root / "bin/docker").write_text("#!/bin/sh\nexit 42\n")
        self.assertEqual(self.build().returncode, 42)
        self.assertFalse((self.root / "bundle").exists())


class OfflineComposeTest(unittest.TestCase):
    @unittest.skipUnless(shutil.which("docker"), "Docker CLI required; no daemon needed")
    def test_service_graph_and_offline_policy(self):
        env = dict(os.environ)
        for line in (REPO / "deploy/.env.example").read_text().splitlines():
            if line and not line.startswith("#"):
                key, value = line.split("=", 1)
                env[key] = value or "offline-test-secret"
        result = subprocess.run(
            ["docker", "compose", "--env-file", str(REPO / "deploy/.env.example"),
             "-f", str(REPO / "deploy/compose.yaml"), "config", "--format", "json"],
            env=env, capture_output=True, text=True, check=True)
        services = json.loads(result.stdout)["services"]
        self.assertEqual(len(services), 6)
        for service in services.values():
            self.assertEqual(service["pull_policy"], "never")
            self.assertNotIn("build", service)
        for name in ("analyzer", "reportd", "grafana"):
            self.assertEqual(services[name]["depends_on"]["migrate"]["condition"],
                             "service_completed_successfully")
        self.assertEqual(services["migrate"]["command"], ["up"])
        self.assertEqual(services["reportd"]["entrypoint"], ["/app/reportd"])
        self.assertEqual(services["reportd"]["image"], services["analyzer"]["image"])
        self.assertFalse(services["timescaledb"].get("ports"))
        self.assertFalse(services["grafana-renderer"].get("ports"))


if __name__ == "__main__":
    unittest.main()
