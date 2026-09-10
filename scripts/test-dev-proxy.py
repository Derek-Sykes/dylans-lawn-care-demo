#!/usr/bin/env python3
"""Check the reviewed dev gateway with isolated local containers and synthetic data.

Requires Docker and Python 3. No external Google requests are made. The gateway
uses a temporary internal CA, verified by the probe. No host ports are published.
Supply --gateway-config when testing outside the business workspace.
"""
import argparse
import http.client
from http.cookies import SimpleCookie
import json
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import tempfile
import time
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[1]
HOST = "dev-demo.xsolutionsmd.com"
ORIGIN = "https://" + HOST
FIXTURE_REVISION = "0123456789abcdef0123456789abcdef01234567"


def docker(*args, check=True):
    result = subprocess.run(["docker", *map(str, args)], capture_output=True, text=True, timeout=90)
    if check and result.returncode:
        raise RuntimeError("An isolated Docker test command failed: " + " ".join(map(str, args[:2])))
    return result.stdout.strip()


def dev_block(path):
    lines = path.read_text(encoding="utf-8").splitlines()
    start = next(i for i, line in enumerate(lines) if line.strip() == HOST + " {")
    depth, selected = 0, []
    for line in lines[start:]:
        selected.append(line)
        depth += line.count("{") - line.count("}")
        if depth == 0:
            break
    if depth != 0 or not any("dylan-dev-admin:8082" in line for line in selected):
        raise RuntimeError("The reviewed dev gateway block is incomplete")
    # Routing directives remain verbatim. Only the listener and certificate source
    # change so tests neither request public certificates nor touch the real gateway.
    selected[0] = "https://" + HOST + ":8443 {"
    selected.insert(1, "    tls internal")
    return "{\n    admin off\n    auto_https disable_redirects\n    skip_install_trust\n}\n\n" + "\n".join(selected) + "\n"


class GatewayConnection(http.client.HTTPSConnection):
    def connect(self):
        connection = socket.create_connection(("gateway", self.port), self.timeout)
        self.sock = self._context.wrap_socket(connection, server_hostname=HOST)


def probe():
    import os
    context = ssl.create_default_context(cafile="/fixture/gateway-root.crt")
    bootstrap = os.environ["TEST_BOOTSTRAP"]
    expected_revision = os.environ["TEST_REVISION"]
    run_id, attempt = "987654321", "2"
    def request(path, method="GET", body=None, *, cookie="", csrf="", origin=None, direct=False):
        connection = (http.client.HTTPConnection("booking", 8081, timeout=8) if direct
                      else GatewayConnection(HOST, 8443, context=context, timeout=8))
        headers = {"Host": HOST}
        if cookie:
            headers["Cookie"] = cookie
        if csrf:
            headers["X-CSRF-Token"] = csrf
        if origin is not None:
            headers["Origin"] = origin
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        try:
            connection.request(method, path, body=data, headers=headers)
            response = connection.getresponse()
            return response.status, dict(response.getheaders()), response.read().decode("utf-8")
        finally:
            connection.close()

    for _ in range(40):
        try:
            if request("/api/admin/session")[0] == 200:
                break
        except (OSError, http.client.HTTPException):
            pass
        time.sleep(.25)
    status, headers, body = request("/")
    assert status == 200 and "Dylan" in body
    assert "noindex" in headers.get("X-Robots-Tag", "").lower()
    assert json.loads(request("/version.json")[2])["revision"] == expected_revision
    status, headers, _ = request("/admin?google=connected")
    assert status == 308 and headers["Location"] == "/admin/?google=connected"
    status, _, body = request("/admin/")
    assert status == 200 and "Owner portal" in body
    for path in ("/admin.css", "/admin.js"):
        status, _, body = request(path)
        assert status == 200 and body
    version = json.loads(request("/admin/version.json")[2])
    assert version == {"revision": expected_revision, "deploymentRunId": run_id, "deploymentRunAttempt": attempt}
    assert request("/api/admin/settings")[0] == 401
    assert request("/api/admin/bootstrap", "POST", {"token": bootstrap}, origin="https://attacker.invalid")[0] == 403
    status, headers, body = request("/api/admin/bootstrap", "POST", {"token": bootstrap}, origin=ORIGIN)
    assert status == 200
    session = json.loads(body)
    cookies = SimpleCookie()
    cookies.load(headers["Set-Cookie"])
    morsel = next(iter(cookies.values()))
    assert morsel["secure"] and morsel["httponly"] and morsel["path"] == "/" and morsel.key.startswith("__Host-")
    cookie, csrf = morsel.key + "=" + morsel.value, session["csrfToken"]
    status, _, body = request("/api/admin/settings", cookie=cookie)
    assert status == 200
    settings = json.loads(body)
    assert request("/api/admin/settings", "PUT", settings, cookie=cookie, origin=ORIGIN)[0] == 403
    assert request("/api/admin/settings", "PUT", settings, cookie=cookie, csrf=csrf, origin=ORIGIN)[0] == 200
    status, _, body = request("/api/admin/google/connect", "POST", {}, cookie=cookie, csrf=csrf, origin=ORIGIN)
    assert status == 200
    authorization = urlsplit(json.loads(body)["url"])
    query = parse_qs(authorization.query)
    assert authorization.scheme == "https" and authorization.hostname == "accounts.google.com"
    assert query["redirect_uri"] == [ORIGIN + "/oauth/callback"]
    assert query["code_challenge_method"] == ["S256"]
    # No browser follows the authorization URL. Candidate network also blocks egress.
    status, headers, _ = request("/oauth/callback?error=access_denied")
    assert status == 303 and headers["Location"] == ORIGIN + "/admin/?google=failed"
    config = json.loads(request("/api/public/config")[2])
    assert config["bookingEnabled"] is False
    for path in ("/api/admin/session", "/api/admin/settings", "/admin/", "/admin.js", "/oauth/callback"):
        assert request(path, direct=True)[0] == 404, "Public listener exposed " + path
    print("PASS: trusted local HTTPS gateway, public/admin routing, assets, dispatch receipt, owner authentication, CSRF, OAuth callback and public-listener isolation.")


def main():
    import sys
    if "--probe" in sys.argv:
        probe()
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--web-image", default="dylan-dev-runtime-web-test")
    parser.add_argument("--booking-image", default="dylan-booking-shared-origin-runtime")
    parser.add_argument("--gateway-image", default="caddy:2-alpine")
    parser.add_argument("--revision", default=FIXTURE_REVISION)
    parser.add_argument("--gateway-config", type=Path,
                        default=ROOT.parents[2] / "xsolutions-website/server/gateway/config/Caddyfile")
    args = parser.parse_args()
    # Resolve the trusted probe image before creating the network with no egress.
    docker("image", "inspect", "python:3.14-alpine")
    name = "dylan-proxy-check-" + secrets.token_hex(5)
    network = name + "-network"
    names = [name + "-gateway", name + "-web", name + "-booking"]
    bootstrap = secrets.token_urlsafe(32)
    run_id, attempt = "987654321", "2"
    with tempfile.TemporaryDirectory(prefix="dylan-proxy-check-") as temporary:
        temporary = Path(temporary)
        gateway_file = temporary / "Caddyfile"
        gateway_file.write_text(dev_block(args.gateway_config), encoding="utf-8")
        try:
            docker("network", "create", "--internal", network)
            docker("run", "-d", "--name", names[2], "--network", network,
                   "--network-alias", "booking", "--network-alias", "dylan-dev-admin",
                   "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                   "--tmpfs", "/tmp", "--tmpfs", "/data:uid=10001,gid=10001,mode=0700",
                   "-e", "PUBLIC_ORIGIN=" + ORIGIN,
                   "-e", "ADMIN_ORIGIN=" + ORIGIN, "-e", "ADMIN_BASE_PATH=/admin",
                   "-e", "GOOGLE_OAUTH_MODE=web", "-e", "BOOTSTRAP_TOKEN=" + bootstrap,
                   "-e", "GOOGLE_CLIENT_ID=fixture.apps.googleusercontent.com",
                   "-e", "GOOGLE_CLIENT_SECRET=synthetic-fixture-secret",
                   "-e", "DEPLOYMENT_RUN_ID=" + run_id, "-e", "DEPLOYMENT_RUN_ATTEMPT=" + attempt,
                   args.booking_image)
            for container, image, alias, config in (
                (names[1], args.web_image, "dylan-dev-web", ROOT / "Caddyfile.local"),
                (names[0], args.gateway_image, "gateway", gateway_file),
            ):
                docker("run", "-d", "--name", container, "--network", network,
                       "--network-alias", alias, "--read-only", "--cap-drop", "ALL",
                       "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges",
                       "--tmpfs", "/tmp", "--tmpfs", "/data", "--tmpfs", "/config",
                       "--mount", "type=bind,source=" + str(config.resolve()) + ",target=/etc/caddy/Caddyfile,readonly",
                       image)
            root_cert = temporary / "gateway-root.crt"
            for _ in range(40):
                cert = docker("exec", names[0], "cat", "/data/caddy/pki/authorities/local/root.crt", check=False)
                if "BEGIN CERTIFICATE" in cert:
                    root_cert.write_text(cert + "\n", encoding="ascii")
                    break
                time.sleep(.25)
            if not root_cert.exists():
                raise RuntimeError("Temporary gateway certificate was not generated")
            output = docker("run", "--rm", "--name", name + "-probe", "--network", network,
                            "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                            "--mount", "type=bind,source=" + str(Path(__file__).resolve()) + ",target=/checks/test-dev-proxy.py,readonly",
                            "--mount", "type=bind,source=" + str(temporary) + ",target=/fixture,readonly",
                            "-e", "TEST_BOOTSTRAP=" + bootstrap, "-e", "TEST_REVISION=" + args.revision,
                            "python:3.14-alpine", "python", "/checks/test-dev-proxy.py", "--probe")
            print(output)
        finally:
            for container in [name + "-probe", *names]:
                docker("rm", "-f", "-v", container, check=False)
            docker("network", "rm", network, check=False)


if __name__ == "__main__":
    main()
