"""Exercise an isolated Compose stack without Google credentials or customer data."""
import json
import os
import urllib.error
import urllib.parse
import urllib.request

public_origin = os.environ["PUBLIC_ORIGIN"]
admin_origin = os.environ["ADMIN_ORIGIN"]
cookie = ""
csrf = ""

def request(path, method="GET", body=None, *, admin=True, expected=200, origin=None, auth=True, csrf_header=True, direct_public=False):
    base = "http://booking:8082" if admin else ("http://booking:8081" if direct_public else "http://web:8080")
    external = admin_origin if admin else public_origin
    headers = {"Host": urllib.parse.urlsplit(external).netloc}
    if method != "GET":
        headers["Origin"] = external if origin is None else origin
    if auth and admin and cookie:
        headers["Cookie"] = cookie
    if auth and csrf_header and csrf:
        headers["X-CSRF-Token"] = csrf
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    raw = response.read()
    assert response.status == expected, f"{method} {path}: expected {expected}, got {response.status}"
    if path.startswith("/api/"):
        assert "no-store" in response.headers.get("Cache-Control", ""), "API cache protection missing"
    decoded = json.loads(raw) if raw and "json" in response.headers.get("Content-Type", "") else None
    return decoded, response.headers

anonymous, _ = request("/api/admin/session", auth=False)
assert anonymous["authenticated"] is False
assert "email" not in anonymous.get("google", {})
request("/api/admin/settings", auth=False, expected=401)
request("/api/admin/bootstrap", "POST", {"token": os.environ["BOOTSTRAP_TOKEN"]},
        origin="https://untrusted.example", auth=False, expected=403)
session, headers = request("/api/admin/bootstrap", "POST", {"token": os.environ["BOOTSTRAP_TOKEN"]}, auth=False)
assert session["authenticated"] is True
csrf = session["csrfToken"]
cookies = headers.get_all("Set-Cookie", [])
assert cookies and any("httponly" in value.lower() for value in cookies), "Admin session must be HttpOnly"
cookie = "; ".join(value.split(";", 1)[0] for value in cookies)
settings, _ = request("/api/admin/settings")
request("/api/admin/settings", "PUT", settings, csrf_header=False, expected=403)
settings["businessName"] = "Isolated launcher integration check"
request("/api/admin/settings", "PUT", settings)
saved, _ = request("/api/admin/settings")
assert saved["businessName"] == settings["businessName"]
bookings, _ = request("/api/admin/bookings")
assert bookings["bookings"] == []
public, _ = request("/api/public/config", admin=False)
assert public["bookingEnabled"] is False, "Fresh unconnected installs must fail closed"
assert len(public["services"]) == 3
request("/api/admin/session", admin=False, expected=404)
request("/oauth/callback", admin=False, expected=404)
request("/booking/web/index.html", admin=False, expected=404)
for path in ("/api/admin/session", "/admin.js", "/oauth/callback"):
    request(path, admin=False, direct_public=True, expected=404)
request("/api/admin/logout", "POST", expected=204)
request("/api/admin/settings", expected=401)
print("Application integration passed: bootstrap, cookies, CSRF, persisted settings, private admin routes and closed availability.")
