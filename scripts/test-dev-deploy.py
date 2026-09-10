#!/usr/bin/env python3
"""Isolated tests for the dev updater. No network, Docker daemon or credentials."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

if os.name == "nt":
    raise SystemExit("Run these Linux server tests in Linux or the documented Docker Python container; native Windows does not provide Linux file permissions.")
SOURCE = Path(__file__).resolve().parents[1] / "server/dev/update-release.py"
spec = importlib.util.spec_from_file_location("dev_update", SOURCE)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def release(run=2, attempt=1, revision="b"):
    return {"schema": 1, "environment": "dev", "revision": revision * 40,
            "runId": run, "runAttempt": attempt,
            "webImage": "ghcr.io/derek-sykes/dylans-lawn-care-dev-web@sha256:" + revision * 64,
            "bookingImage": "ghcr.io/derek-sykes/dylans-lawn-care-dev-booking@sha256:" + revision * 64}


class FakeUpdater(module.Updater):
    def __init__(self, base, payload=None):
        super().__init__(base / "root", base / "state", base / "share")
        for path in (self.root, self.state, self.share):
            path.mkdir()
        (self.root / "runtime.env").write_text("PRIVATE_FIXTURE=synthetic\n")
        (self.root / "runtime.env").chmod(0o600)
        (self.share / "compose.yaml").write_text("next compose")
        (self.share / "Caddyfile.local").write_text("next caddy")
        self.payload = payload
        self.calls = []
        self.fail = ""
        self.volume = None
        self.snapshot_data = None

    def seed(self):
        old = release(1, revision="a")
        (self.root / "compose.yaml").write_text("old compose")
        (self.root / "Caddyfile.local").write_text("old caddy")
        (self.root / "release.env").write_text("old images")
        (self.state / "current.json").write_text(json.dumps(old))
        self.volume = "old database, owner and encryption key"
        return old

    def download(self):
        if self.payload is not None:
            (self.work / "deployment.json").write_text(json.dumps(self.payload))
            return module.manifest(self.work / "deployment.json")

    def run(self, *args, **kwargs):
        self.calls.append(tuple(map(str, args)))
        if args[:3] == ("docker", "volume", "inspect"):
            return subprocess.CompletedProcess(args, int(self.volume is None), "", "")
        if args[:3] == ("docker", "volume", "rm"):
            self.volume = None
        return subprocess.CompletedProcess(args, 0, "", "")

    def identity(self, image, revision):
        self.calls.append(("identity", image, revision))

    def test_candidate(self, payload):
        self.calls.append(("candidate",))
        if self.fail == "candidate":
            raise module.DeploymentError("candidate failed")

    def compose(self, *args, **kwargs):
        self.calls.append(("compose", *args))
        current = (self.root / "compose.yaml").read_text()
        if args[0] == "config" and self.fail == "config":
            raise module.DeploymentError("invalid new compose")
        if args[0] == "up" and current == "next compose":
            self.volume = "new schema and new data"
            if self.fail == "up":
                raise module.DeploymentError("new container failed")
        if args[0] == "up" and current == "old compose" and self.fail == "recovery":
            raise module.DeploymentError("old container failed")

    def snapshot(self):
        self.calls.append(("snapshot",))
        if self.fail == "snapshot":
            raise module.DeploymentError("snapshot failed")
        self.snapshot_data = self.volume
        (self.work / "data.tgz").write_text(self.volume)
        self.snapshot_ready = True

    def restore_data(self):
        self.calls.append(("restore",))
        self.volume = self.snapshot_data

    def live(self, payload):
        self.calls.append(("live", payload["runId"]))
        if payload["runId"] == 2 and self.fail in ("live", "recovery"):
            raise module.DeploymentError("live check failed")


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.base = Path(self.temporary.name)
        self.signals = patch.object(module.signal, "signal")
        self.signals.start()

    def tearDown(self):
        self.signals.stop()
        self.temporary.cleanup()

    def updater(self, payload=None):
        return FakeUpdater(self.base, payload)

    def assert_prior(self, updater):
        self.assertEqual(json.loads((updater.state / "current.json").read_text()), release(1, revision="a"))
        self.assertEqual((updater.root / "compose.yaml").read_text(), "old compose")
        self.assertEqual(updater.volume, "old database, owner and encryption key")
        self.assertFalse(list(updater.state.glob("check.*")))

    def test_no_manual_asset_is_noop(self):
        updater = self.updater()
        updater.seed()
        self.assertIn("No manual release", updater.execute())
        self.assert_prior(updater)
        self.assertFalse(any(call[0] in ("identity", "compose") for call in updater.calls))

    def test_first_deployment(self):
        updater = self.updater(release())
        self.assertIn("Deployed and verified", updater.execute())
        self.assertEqual(module.manifest(updater.state / "current.json"), release())
        self.assertIn("DYLAN_DEV_RUN_ID=2", (updater.root / "release.env").read_text())
        self.assertFalse((updater.state / "previous").exists())

    def test_second_deployment_retains_one_complete_previous_generation(self):
        updater = self.updater(release())
        updater.seed()
        updater.execute()
        self.assertEqual((updater.state / "previous/data.tgz").read_text(), "old database, owner and encryption key")
        self.assertEqual(module.manifest(updater.state / "previous/previous-current.json"), release(1, revision="a"))
        self.assertEqual(module.manifest(updater.state / "current.json"), release())
        calls = [call[0] if call[0] != "compose" else call[1] for call in updater.calls]
        self.assertLess(calls.index("candidate"), calls.index("stop"))
        self.assertLess(calls.index("stop"), calls.index("snapshot"))
        self.assertLess(calls.index("snapshot"), calls.index("up"))

    def test_equal_and_older_dispatch_never_pull(self):
        updater = self.updater(release(1, revision="a"))
        updater.seed()
        updater.execute()
        self.assertFalse(any(call[0] == "identity" for call in updater.calls))
        self.assert_prior(updater)

    def test_same_revision_new_manual_attempt_still_deploys(self):
        updater = self.updater(release(1, attempt=2, revision="a"))
        updater.seed()
        updater.execute()
        self.assertEqual(module.manifest(updater.state / "current.json")["runAttempt"], 2)
        self.assertTrue(any(call[0] == "candidate" for call in updater.calls))

    def test_changed_completed_manifest_refused(self):
        updater = self.updater(release(1, revision="b"))
        updater.seed()
        with self.assertRaisesRegex(module.DeploymentError, "changed after deployment"):
            updater.execute()
        self.assert_prior(updater)

    def test_candidate_failure_keeps_live_data_untouched(self):
        updater = self.updater(release())
        updater.seed()
        updater.fail = "candidate"
        with self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assert_prior(updater)
        self.assertFalse(any(call[0] == "compose" for call in updater.calls))

    def test_failed_replacement_restores_data_and_state(self):
        updater = self.updater(release())
        updater.seed()
        updater.fail = "up"
        with self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assert_prior(updater)
        self.assertIn(("restore",), updater.calls)
        self.assertIn(("live", 1), updater.calls)

    def test_failed_https_restores_data_and_state(self):
        updater = self.updater(release())
        updater.seed()
        updater.fail = "live"
        with self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assert_prior(updater)

    def test_invalid_new_compose_still_uses_previous_definition_for_recovery(self):
        updater = self.updater(release())
        updater.seed()
        updater.fail = "config"
        with self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assert_prior(updater)
        self.assertIn(("live", 1), updater.calls)

    def test_failed_first_release_removes_only_fresh_dev_data(self):
        updater = self.updater(release())
        updater.fail = "live"
        with self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assertIsNone(updater.volume)
        self.assertFalse((updater.state / "current.json").exists())
        self.assertFalse((updater.root / "release.env").exists())
        self.assertTrue((updater.root / "runtime.env").is_file())
        self.assertIn(("docker", "volume", "rm", "dylan-dev_booking-data"), updater.calls)

    def test_snapshot_failure_restarts_previous_without_restoring_partial_backup(self):
        updater = self.updater(release())
        updater.seed()
        updater.fail = "snapshot"
        with self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assert_prior(updater)
        self.assertNotIn(("restore",), updater.calls)
        self.assertIn(("live", 1), updater.calls)

    def test_interruption_after_state_rename_restores_previous_manifest(self):
        updater = self.updater(release())
        updater.seed()
        replace = os.replace
        interrupted = False

        def fail_after_rename(source, target):
            nonlocal interrupted
            replace(source, target)
            if Path(target).name == "current.json" and not interrupted:
                interrupted = True
                raise module.DeploymentError("handled interruption")

        with patch.object(module.os, "replace", fail_after_rename):
            with self.assertRaises(module.DeploymentError):
                updater.execute()
        self.assert_prior(updater)

    def test_recovery_failure_retains_private_recovery_files(self):
        updater = self.updater(release())
        updater.seed()
        updater.fail = "recovery"
        with patch("sys.stderr"), self.assertRaises(module.DeploymentError):
            updater.execute()
        self.assertTrue(updater.retained)
        self.assertTrue((updater.work / "data.tgz").is_file())
        self.assertTrue((updater.work / "previous-current.json").is_file())
        self.assertTrue((updater.state / "transaction.json").is_file())

    def test_unhandled_process_loss_blocks_later_deploy_until_inspected(self):
        updater = self.updater(release())
        updater.seed()
        (updater.state / "transaction.json").write_text('{"workDirectory":"check.saved"}')
        with self.assertRaisesRegex(module.DeploymentError, "interrupted dev deployment"):
            updater.execute()
        self.assertFalse(updater.calls)
        self.assertEqual(updater.volume, "old database, owner and encryption key")
        self.assertTrue((updater.state / "transaction.json").is_file())

    def test_orphaned_volume_or_partial_install_refused(self):
        updater = self.updater(release())
        updater.volume = "do not touch"
        with self.assertRaisesRegex(module.DeploymentError, "no recovery state"):
            updater.execute()
        self.assertEqual(updater.volume, "do not touch")
        (updater.root / "release.env").write_text("incomplete")
        with self.assertRaisesRegex(module.DeploymentError, "Incomplete"):
            updater.execute()

    def test_manifest_validation(self):
        path = self.base / "manifest.json"
        bad = [{**release(), "runId": True}, {**release(), "runAttempt": 0},
               {**release(), "environment": "production"}, {**release(), "schema": True},
               {**release(), "extra": "field"}, {**release(), "webImage": "evil:latest"},
               {**release(), "bookingImage": release()["webImage"]},
               {**release(), "revision": "x" * 40}]
        for data in bad:
            with self.subTest(data=data):
                path.write_text(json.dumps(data))
                with self.assertRaises(module.DeploymentError):
                    module.manifest(path)
        path.write_text(" " * 4097)
        with self.assertRaisesRegex(module.DeploymentError, "Oversized"):
            module.manifest(path)

    def test_image_identity_rejects_wrong_arch_source_and_revision(self):
        updater = self.updater()
        valid = {"Architecture": "arm64", "Os": "linux", "Config": {"Labels": {
            "org.opencontainers.image.source": module.SOURCE,
            "org.opencontainers.image.revision": "b" * 40}}}
        for change in ("architecture", "source", "revision"):
            data = json.loads(json.dumps(valid))
            if change == "architecture":
                data["Architecture"] = "amd64"
            else:
                data["Config"]["Labels"]["org.opencontainers.image." + change] = "unexpected"
            result = subprocess.CompletedProcess([], 0, json.dumps([data]), "")
            with self.subTest(change=change), patch.object(updater, "run", return_value=result):
                with self.assertRaises(module.DeploymentError):
                    module.Updater.identity(updater, release()["webImage"], "b" * 40)

    def test_command_errors_do_not_echo_private_output(self):
        updater = self.updater()
        result = subprocess.CompletedProcess([], 1, "PRIVATE_SYNTHETIC_STDOUT", "PRIVATE_SYNTHETIC_STDERR")
        with patch.object(module.subprocess, "run", return_value=result):
            with self.assertRaises(module.DeploymentError) as error:
                module.Updater.run(updater, "docker", "compose")
        self.assertNotIn("PRIVATE_SYNTHETIC", str(error.exception))

    def test_candidates_are_isolated_from_live_data_credentials_and_proxy(self):
        updater = self.updater()
        updater.work = updater.state / "candidate-check"
        updater.work.mkdir()
        def response(name, port, path):
            body = json.dumps({"revision": "b" * 40}) if path == "/version.json" else "Owner portal"
            return subprocess.CompletedProcess([], 0, body, "X-Robots-Tag: noindex")
        with patch.object(updater, "healthy"), patch.object(updater, "candidate_request", side_effect=response):
            module.Updater.test_candidate(updater, release())
        commands = [call for call in updater.calls if call[:2] == ("docker", "run")]
        self.assertEqual(len(commands), 2)
        for command in commands:
            joined = " ".join(command)
            self.assertNotIn(module.VOLUME, joined)
            self.assertNotIn("runtime.env", joined)
            self.assertNotIn("xsolutions-proxy", joined)
            self.assertNotIn("GOOGLE_CLIENT_SECRET", joined)
            self.assertIn("--read-only", command)
            self.assertNotIn("-p", command)
        self.assertIn(("docker", "network", "create", "--internal", updater.candidate_network), updater.calls)


if __name__ == "__main__":
    unittest.main(verbosity=2)
