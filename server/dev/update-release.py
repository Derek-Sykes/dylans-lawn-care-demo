#!/usr/bin/env python3
"""Root-owned dev updater. Downloads are validated data, never executable code."""
import contextlib
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time

REPO = "Derek-Sykes/dylans-lawn-care-demo"
ORIGIN = "https://dev-demo.xsolutionsmd.com"
SOURCE = "https://github.com/" + REPO
MANIFEST = SOURCE + "/releases/download/dev-server/deployment.json"
PROJECT = "dylan-dev"
VOLUME = PROJECT + "_booking-data"
FIELDS = {"schema", "environment", "revision", "runId", "runAttempt", "webImage", "bookingImage"}


class DeploymentError(Exception):
    pass


def manifest(path):
    if path.stat().st_size > 4096:
        raise DeploymentError("Oversized deployment manifest.")
    try:
        data = json.loads(path.read_text())
        if not isinstance(data, dict) or set(data) != FIELDS:
            raise ValueError()
        if type(data["schema"]) is not int or data["schema"] != 1 or data["environment"] != "dev":
            raise ValueError()
        if not re.fullmatch(r"[0-9a-f]{40}", data["revision"]):
            raise ValueError()
        for field in ("runId", "runAttempt"):
            if type(data[field]) is not int or not 0 < data[field] < 10**18:
                raise ValueError()
        for field, suffix in (("webImage", "web"), ("bookingImage", "booking")):
            if not re.fullmatch(r"ghcr.io/derek-sykes/dylans-lawn-care-dev-" + suffix + r"@sha256:[0-9a-f]{64}", data[field]):
                raise ValueError()
    except (ValueError, TypeError):
        raise DeploymentError("Invalid deployment manifest.") from None
    return data


def sequence(data):
    return data["runId"], data["runAttempt"]


def sync_directory(path):
    # Server is Linux; Windows is supported only by the synthetic Docker test harness.
    if os.name == "nt":
        return
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def publish_file(source, target):
    with source.open("r+b") as file:
        os.fsync(file.fileno())
    os.replace(source, target)
    sync_directory(target.parent)


class Updater:
    def __init__(self, root=Path("/opt/dylan-dev"), state=Path("/var/lib/dylan-dev-deploy"),
                 share=Path("/usr/local/share/dylan-dev")):
        self.root, self.state, self.share = root, state, share
        self.work = None
        self.old = None
        self.started = False
        self.activated = False
        self.snapshot_ready = False
        self.committed = False
        self.retained = False
        self.candidate = PROJECT + "-candidate-" + str(os.getpid())
        self.candidate_network = self.candidate + "-network"

    def run(self, *args, timeout=120, check=True):
        # Never include command output in errors: Docker may include runtime environment.
        try:
            result = subprocess.run([str(x) for x in args], capture_output=True, text=True,
                                    encoding="utf-8", errors="replace", timeout=timeout, check=False)
        except (OSError, subprocess.TimeoutExpired):
            raise DeploymentError("A bounded deployment command failed or timed out.") from None
        if check and result.returncode:
            raise DeploymentError("A deployment command failed; existing recovery files were preserved when needed.")
        return result

    def compose(self, *args, check=True):
        return self.run("docker", "compose", "--project-name", PROJECT, "--project-directory", self.root,
                        "--env-file", self.root / "release.env", "-f", self.root / "compose.yaml",
                        *args, timeout=150, check=check)

    def download(self):
        result = self.run("curl", "-LsS", "--proto", "=https", "--proto-redir", "=https",
                          "--connect-timeout", "15", "--max-time", "60", "--max-filesize", "4096",
                          "--retry", "2", "-w", "%{http_code}", "-o", self.work / "deployment.json",
                          MANIFEST + "?check=" + str(int(time.time())), timeout=190)
        status = result.stdout.strip()
        if status == "404":
            return None
        if status != "200":
            raise DeploymentError("Unable to download the manually published manifest.")
        return manifest(self.work / "deployment.json")

    def identity(self, image, revision):
        self.run("docker", "pull", image, timeout=240)
        result = self.run("docker", "image", "inspect", image)
        try:
            data = json.loads(result.stdout)[0]
            if data["Architecture"] != "arm64" or data["Os"] != "linux":
                raise ValueError()
            if data["Config"]["Labels"]["org.opencontainers.image.revision"] != revision:
                raise ValueError()
            if data["Config"]["Labels"]["org.opencontainers.image.source"] != SOURCE:
                raise ValueError()
        except (ValueError, KeyError, TypeError, IndexError):
            raise DeploymentError("Image architecture, source or revision does not match the approved release.") from None

    def request(self, url, *, host=None, resolve=False, path="probe"):
        args = ["curl", "-fsS", "--connect-timeout", "3", "--max-time", "8", "--max-filesize", "1048576"]
        if host:
            args += ["-H", "Host: " + host]
        if resolve:
            args += ["--resolve", "dev-demo.xsolutionsmd.com:443:127.0.0.1"]
        args += ["-D", self.work / (path + ".headers"), url]
        return self.run(*args, timeout=12).stdout

    def revision(self, url, expected, **kwargs):
        try:
            actual = json.loads(self.request(url, **kwargs))
            if actual.get("revision") != expected:
                raise ValueError()
        except (ValueError, AttributeError):
            raise DeploymentError("The served revision does not match the requested image.") from None

    def healthy(self, name):
        for _ in range(45):
            result = self.run("docker", "inspect", name, "--format", "{{.State.Health.Status}}", check=False)
            if result.returncode == 0 and result.stdout.strip() == "healthy":
                return
            time.sleep(1)
        raise DeploymentError("The isolated candidate did not become healthy.")

    def candidate_request(self, name, port, path):
        # Internal Docker networks intentionally publish no host ports. Probe inside the container.
        return self.run("docker", "exec", name, "wget", "-q", "-S", "-T", "8", "-O", "-",
                        "--header", "Host: dev-demo.xsolutionsmd.com", "http://127.0.0.1:" + str(port) + path,
                        timeout=12)

    def test_candidate(self, release):
        self.run("docker", "network", "create", "--internal", self.candidate_network)
        self.run("docker", "run", "-d", "--name", self.candidate + "-booking", "--network", self.candidate_network,
                 "--network-alias", "booking", "--read-only", "--tmpfs", "/tmp",
                 "--tmpfs", "/data:uid=10001,gid=10001,mode=0700", "--cap-drop", "ALL",
                 "--security-opt", "no-new-privileges",
                 "-e", "PUBLIC_ORIGIN=" + ORIGIN, "-e", "ADMIN_ORIGIN=" + ORIGIN, "-e", "ADMIN_BASE_PATH=/admin",
                 "-e", "GOOGLE_OAUTH_MODE=web", "-e", "BOOTSTRAP_TOKEN=" + secrets.token_urlsafe(32),
                 release["bookingImage"])
        self.run("docker", "run", "-d", "--name", self.candidate + "-web", "--network", self.candidate_network,
                 "--read-only", "--tmpfs", "/tmp", "--tmpfs", "/data", "--tmpfs", "/config",
                 "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges",
                 "--mount", "type=bind,source=" + str(self.share / "Caddyfile.local") + ",target=/etc/caddy/Caddyfile,readonly",
                 release["webImage"])
        self.healthy(self.candidate + "-booking")
        self.healthy(self.candidate + "-web")
        for name, port in ((self.candidate + "-booking", 8082), (self.candidate + "-web", 8080)):
            try:
                actual = json.loads(self.candidate_request(name, port, "/version.json").stdout)
                if actual.get("revision") != release["revision"]:
                    raise ValueError()
            except (ValueError, AttributeError):
                raise DeploymentError("Candidate revision does not match the requested image.") from None
        page = self.candidate_request(self.candidate + "-web", 8080, "/")
        if not page.stdout.strip():
            raise DeploymentError("Candidate public page was empty.")
        if "noindex" not in page.stderr.lower():
            raise DeploymentError("The development website must remain noindex.")
        admin = self.candidate_request(self.candidate + "-booking", 8082, "/admin/")
        if "Owner portal" not in admin.stdout:
            raise DeploymentError("Candidate admin route did not serve the owner portal.")
        self.candidate_request(self.candidate + "-web", 8080, "/api/public/config")
        self.remove_candidates()

    def remove_candidates(self):
        self.run("docker", "rm", "-f", "-v", self.candidate + "-web", self.candidate + "-booking", check=False)
        self.run("docker", "network", "rm", self.candidate_network, check=False)

    def snapshot(self):
        self.run("docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "tar",
                 "--mount", "type=volume,source=" + VOLUME + ",target=/data,readonly",
                 "--mount", "type=bind,source=" + str(self.work) + ",target=/backup",
                 self.old["bookingImage"], "-czpf", "/backup/data.tgz", "-C", "/data", ".", timeout=120)
        archive = self.work / "data.tgz"
        if not archive.is_file() or archive.stat().st_size == 0:
            raise DeploymentError("A complete stopped-database snapshot was not created.")
        with archive.open("r+b") as file:
            os.fsync(file.fileno())
        sync_directory(self.work)
        self.snapshot_ready = True

    def restore_data(self):
        # Fixed script, exact app volume; never interpolates manifest text as shell code.
        restore = "find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -xzpf /backup/data.tgz -C /data"
        self.run("docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh",
                 "--mount", "type=volume,source=" + VOLUME + ",target=/data",
                 "--mount", "type=bind,source=" + str(self.work) + ",target=/backup,readonly",
                 self.old["bookingImage"], "-c", restore, timeout=120)

    def live(self, release):
        deadline = time.monotonic() + 90
        for _ in range(15):
            try:
                self.revision(ORIGIN + "/version.json", release["revision"], resolve=True)
                admin_version = json.loads(self.request(ORIGIN + "/admin/version.json", resolve=True))
                if (admin_version.get("revision") != release["revision"] or
                    admin_version.get("deploymentRunId") != str(release["runId"]) or
                    admin_version.get("deploymentRunAttempt") != str(release["runAttempt"])):
                    raise DeploymentError("Live admin must identify the exact manual dispatch.")
                if not self.request(ORIGIN + "/", resolve=True, path="live").strip():
                    raise DeploymentError("Live public page was empty.")
                if "noindex" not in (self.work / "live.headers").read_text().lower():
                    raise DeploymentError("Live dev site is missing noindex.")
                if "Owner portal" not in self.request(ORIGIN + "/admin/", resolve=True):
                    raise DeploymentError("Live admin route did not serve the owner portal.")
                self.request(ORIGIN + "/api/public/config", resolve=True)
                return
            except (DeploymentError, ValueError, AttributeError):
                if time.monotonic() >= deadline:
                    break
                time.sleep(2)
        raise DeploymentError("Both public and admin HTTPS routes must serve the requested release.")

    def rollback(self):
        if self.old:
            # Recover a known-valid definition before using Compose to stop a failed generation.
            for name in ("compose.yaml", "release.env", "Caddyfile.local"):
                shutil.copy2(self.work / ("previous-" + name), self.root / name)
            self.compose("stop", "--timeout", "20")
            if self.snapshot_ready:
                self.restore_data()
            shutil.copy2(self.work / "previous-current.json", self.state / "current.json.new")
            publish_file(self.state / "current.json.new", self.state / "current.json")
            self.compose("up", "-d", "--no-build", "--wait", "--wait-timeout", "90")
            self.live(self.old)
        else:
            if self.activated:
                self.compose("down", "--timeout", "20")
            if self.run("docker", "volume", "inspect", VOLUME, check=False).returncode == 0:
                self.run("docker", "volume", "rm", VOLUME)
            for name in ("compose.yaml", "release.env", "Caddyfile.local"):
                (self.root / name).unlink(missing_ok=True)
            (self.state / "current.json").unlink(missing_ok=True)
            (self.state / "current.json.new").unlink(missing_ok=True)

    def prior_state(self):
        names = (self.root / "compose.yaml", self.root / "release.env", self.root / "Caddyfile.local", self.state / "current.json")
        present = [x.is_file() and not x.is_symlink() for x in names]
        if any(present) and not all(present):
            raise DeploymentError("Incomplete previous dev deployment; preserve its data and repair state first.")
        if all(present):
            self.old = manifest(self.state / "current.json")
            for path in names:
                shutil.copy2(path, self.work / ("previous-" + path.name))
            return
        result = self.run("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + PROJECT)
        volume = self.run("docker", "volume", "inspect", VOLUME, check=False)
        if result.stdout.strip() or volume.returncode == 0:
            raise DeploymentError("Existing dev containers or data have no recovery state; refusing to overwrite them.")

    def deploy(self, release):
        self.started = True
        # A process-kill/reboot must never let a later poll overwrite the recovery generation.
        # Handled failures recover automatically; an unhandled process loss requires inspection.
        journal = {"workDirectory": self.work.name, "requestedRelease": release}
        (self.state / "transaction.json.new").write_text(json.dumps(journal))
        publish_file(self.state / "transaction.json.new", self.state / "transaction.json")
        if self.old:
            self.compose("stop", "--timeout", "20")
            self.snapshot()
        shutil.copy2(self.share / "compose.yaml", self.root / "compose.yaml")
        shutil.copy2(self.share / "Caddyfile.local", self.root / "Caddyfile.local")
        (self.root / "release.env").write_text("DYLAN_DEV_WEB_IMAGE=" + release["webImage"] +
                                               "\nDYLAN_DEV_BOOKING_IMAGE=" + release["bookingImage"] +
                                               "\nDYLAN_DEV_RUN_ID=" + str(release["runId"]) +
                                               "\nDYLAN_DEV_RUN_ATTEMPT=" + str(release["runAttempt"]) + "\n")
        self.compose("config", "--quiet")
        self.activated = True
        self.compose("up", "-d", "--no-build", "--wait", "--wait-timeout", "90")
        self.live(release)
        shutil.copy2(self.work / "deployment.json", self.state / "current.json.new")
        publish_file(self.state / "current.json.new", self.state / "current.json")
        self.committed = True
        if self.old:
            # One complete prior generation including its encryption key and stopped SQLite files.
            previous = self.state / "previous"
            self.retained = True
            if previous.exists():
                shutil.rmtree(previous)
            os.replace(self.work, previous)
            self.work = previous
            sync_directory(self.state)
        (self.state / "transaction.json").unlink()
        sync_directory(self.state)

    def execute(self):
        if (self.state / "transaction.json").exists():
            raise DeploymentError("An interrupted dev deployment needs inspection; transaction.json identifies its retained recovery files. No new release was applied.")
        self.work = Path(tempfile.mkdtemp(prefix="check.", dir=self.state))
        try:
            release = self.download()
            if release is None:
                return "No manual release has been published."
            self.prior_state()
            if self.old and sequence(release) <= sequence(self.old):
                if sequence(release) == sequence(self.old) and release != self.old:
                    raise DeploymentError("A published run changed after deployment; refusing to replace it.")
                return "The latest manually requested release is already applied."
            runtime = self.root / "runtime.env"
            if (not runtime.is_file() or runtime.is_symlink() or runtime.stat().st_mode & 0o077
                or runtime.stat().st_uid != os.geteuid()):
                raise DeploymentError("Prepare root-private /opt/dylan-dev/runtime.env before deployment.")
            self.run("docker", "network", "inspect", "xsolutions-proxy")
            self.identity(release["webImage"], release["revision"])
            self.identity(release["bookingImage"], release["revision"])
            self.test_candidate(release)
            self.deploy(release)
            return "Deployed and verified manually requested dev revision " + release["revision"] + "."
        except BaseException:
            if self.started and not self.committed:
                # Let an in-progress recovery finish after the first handled interruption.
                signal.signal(signal.SIGTERM, signal.SIG_IGN)
                signal.signal(signal.SIGINT, signal.SIG_IGN)
                try:
                    self.rollback()
                    (self.state / "transaction.json").unlink(missing_ok=True)
                    (self.state / "transaction.json.new").unlink(missing_ok=True)
                    sync_directory(self.state)
                except BaseException:
                    self.retained = True
                    print("Recovery failed; files retained at " + str(self.work) + ". Inspect dylan-dev-update.service.", file=sys.stderr)
            raise
        finally:
            with contextlib.suppress(DeploymentError):
                self.remove_candidates()
            if not self.retained:
                shutil.rmtree(self.work)


def interrupted(signum, _frame):
    raise DeploymentError("Deployment interrupted; restoring the previous generation when required.")


def main():
    if os.geteuid() != 0:
        raise DeploymentError("Run the installed updater as root.")
    os.umask(0o077)
    updater = Updater()
    for path in (updater.root, updater.state, updater.share):
        if not path.is_dir() or path.is_symlink() or path.stat().st_uid != 0 or path.stat().st_mode & 0o022:
            raise DeploymentError("Deployment directories must be installed and owned by root.")
    with (updater.state / "update.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        signal.signal(signal.SIGTERM, interrupted)
        signal.signal(signal.SIGINT, interrupted)
        print(updater.execute())


if __name__ == "__main__":
    try:
        main()
    except DeploymentError as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
