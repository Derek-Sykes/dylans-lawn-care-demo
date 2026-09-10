#!/usr/bin/env python3
"""Developer-only real Docker checks with synthetic, disposable state.

Build the web and booking images with the same REVISION, then run:
python scripts/test-dev-deploy-containers.py WEB_IMAGE BOOKING_IMAGE REVISION
No remote access, real credentials, existing containers or existing data are used.
"""
import importlib.util
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
import types
import uuid

if os.name == "nt":
    sys.modules.setdefault("fcntl", types.SimpleNamespace())
repo = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("dev_update", repo / "server/dev/update-release.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def main():
    if len(sys.argv) != 4 or not re.fullmatch(r"[0-9a-f]{40}", sys.argv[3]):
        raise SystemExit("Pass WEB_IMAGE BOOKING_IMAGE and the common 40-character REVISION.")
    web, booking, revision = sys.argv[1:]
    token = uuid.uuid4().hex[:12]
    module.PROJECT = "dylan-dev-runtime-test-" + token
    module.VOLUME = module.PROJECT + "_booking-data"
    with tempfile.TemporaryDirectory(prefix="dylan-dev-runtime-test-") as directory:
        base = Path(directory)
        root, state, share = base / "root", base / "state", base / "share"
        for path in (root, state, share):
            path.mkdir()
        shutil.copy2(repo / "Caddyfile.local", share / "Caddyfile.local")
        updater = module.Updater(root, state, share)
        updater.work = state / "check"
        updater.work.mkdir()
        try:
            updater.test_candidate({"revision": revision, "webImage": web, "bookingImage": booking})
            print("Both isolated candidate containers passed health, served revision and noindex checks.")
            updater.run("docker", "volume", "create", module.VOLUME)
            mount = "type=volume,source=" + module.VOLUME + ",target=/data"
            updater.run("docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh",
                        "--mount", mount, booking, "-c",
                        "printf old-data > /data/booking.db; printf synthetic-key > /data/credential.key; chown -R 10001:10001 /data")
            updater.old = {"bookingImage": booking}
            updater.snapshot()
            updater.run("docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh",
                        "--mount", mount, booking, "-c", "printf changed > /data/booking.db; touch /data/new-schema-file")
            updater.restore_data()
            result = updater.run("docker", "run", "--rm", "--network", "none", "--entrypoint", "sh",
                                 "--mount", mount, booking, "-c",
                                 "test ! -e /data/new-schema-file && test $(cat /data/booking.db) = old-data && test $(cat /data/credential.key) = synthetic-key && test -w /data/booking.db")
            assert result.returncode == 0
            print("Stopped-volume snapshot/restore preserved synthetic database, key and owner permissions; removed new-generation files.")
        finally:
            updater.remove_candidates()
            updater.run("docker", "volume", "rm", module.VOLUME, check=False)


if __name__ == "__main__":
    main()
