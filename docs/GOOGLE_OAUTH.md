# Google Calendar connection

The local application uses a Google **Desktop app** OAuth client and a loopback callback at `http://127.0.0.1:<admin-port>/oauth/callback`. The launcher chooses the port, so installations do not need a fixed port or a registered callback for every laptop.

`booking/config/google-client.json` contains the app's public client identifier. It grants no access to a Google account by itself. Account access requires the owner's consent and the application's PKCE verifier. Private credentials must never enter this public repository or its images. Browser sessions and personal Google tokens stay in each installation's local data volume.

## First connection on a new computer

Run `./website.ps1 start` in PowerShell or `./website start` in Bash. After the containers become healthy, the launcher checks whether the installation already has Google application configuration. If it is missing, Git retrieves `google-client.json` from the `main` branch of the private `Derek-Sykes/xsolutions-booking-private` repository using the host's existing authentication. The launcher passes the file through standard input to the booking container, which validates and encrypts it in the persistent local database. The temporary fetch is removed before startup finishes.

The owner then chooses **Connect Google Calendar** and approves the two Calendar permissions. No manual JSON import or environment editing is needed on an authorized laptop. `dev` and `update` use the same setup check. Existing configured installations do not fetch private configuration again; normal source updates preserve it alongside the owner's Google connection, account and customer records.

The Git account must have read access to the private repository. Existing Git/Git Credential Manager authentication is reused; being signed into GitHub in a browser alone does not establish that authentication. The public source remains clonable by anyone, but private automatic setup is available only to authorized accounts. The launchers do not install credential helpers, change global Git authentication, or forward GitHub credentials into the booking container. A missing file, denied access or invalid configuration stops startup before browser opening, with a safe error. Local runtime data is preserved.

## One-time operator provisioning

Keep `Derek-Sykes/xsolutions-booking-private` private. Add the actual desktop OAuth configuration as `google-client.json` at the root of its `main` branch, and grant read access only to the GitHub accounts permitted to initialize installations. This is central operator setup, not a step repeated on each laptop.

The file can use Google's downloaded desktop-client format (`installed.client_id` and `installed.client_secret`) or a JSON object with `client_id` and `client_secret`. It must be no larger than 64 KiB, and its identifier must match the public client configured by the application. Do not store personal Google refresh tokens, customer information, local bootstrap credentials, databases or GitHub authentication tokens in that repository. Do not put the private configuration under this public project's `dist/` or `booking/` directories.

The registered Google endpoint requires the desktop client secret. On September 10, 2026, both a token-endpoint preflight and the complete browser consent flow rejected a PKCE exchange without it with `client_secret is missing`. Automatic private retrieval supplies that required application configuration without exposing it in the public source. Supporting arbitrary public cloners without granting private repository access would require a separate distribution or OAuth service; none is deployed by this project.

The encrypted configuration is never returned by the admin API. The authenticated owner's import/replace controls remain available for recovery or rotation. `GOOGLE_CLIENT_SECRET` supplied privately through the environment or ignored `.env` takes precedence over stored configuration. Copying only the public source to a different computer creates a new installation: it retrieves application configuration through authorized Git and asks for that installation's own Google consent.

## Permissions and calendar behavior

The application requests:

- `openid` and `email` to recognize the connected owner.
- `https://www.googleapis.com/auth/calendar.freebusy` to check busy times.
- `https://www.googleapis.com/auth/calendar.app.created` to manage its dedicated booking calendar.

Appointments appear in a separate booking calendar within the owner's Google Calendar. Availability checks include the owner's primary calendar and the dedicated booking calendar. Other secondary calendars are not included in this first version. The application cannot edit personal events in the primary calendar and does not request Gmail access.

Working hours define the appointment window. Weekly or date-specific time off, Google busy events and existing reservations remove times from that window. Manual availability never overrides a Calendar conflict. The application checks Google again before saving a request, but Google has no atomic reservation operation: an owner could still create a conflicting personal event at exactly the same moment. Direct edits to Google events are not continuously mirrored into the portal's follow-up statuses; manage booking cancellations in the portal.

The initial connection binds the installation to one Google identity. Signing out and signing back in does not transfer ownership to another account. The owner can disconnect and reconnect the same account. The dedicated calendar identifier is preserved so reconnecting does not intentionally create another calendar.

Tokens and the owner identity are encrypted in the local data volume. Updates preserve that volume. Google can still revoke or expire a grant; in that case the application asks the owner to reconnect and stops accepting appointments until calendar access is restored. Keeping files through an update cannot override Google's grant-expiration rules.

## App registration

The maintained client belongs to the `xsolutions-booking` Google Cloud project, with the Calendar API enabled. This registration is separate from other applications. The [privacy notice](PRIVACY.md) describes the software's data handling.

Google's registration status is independent of a website deployment. As checked September 10, 2026, this client has an **External / In production** audience. Google's Verification Center states that data-access verification is not required because all four requested scopes are non-sensitive. Branding has not been verified, so Google may show the application's domain instead of its chosen name. This does not represent a deployed booking website.

An app changed back to **Testing** accepts only registered test accounts and Calendar refresh grants normally expire after seven days. Production access and verification are managed by Google; do not describe a client as verified simply because its audience setting says Production. The completed validation record documents the actual real sign-in result for this release.

## A future hosted installation

A remotely hosted admin domain needs a separate **Web application** OAuth client with its exact HTTPS callback registered, for example `https://admin.example.com/oauth/callback`. Configure `GOOGLE_OAUTH_MODE=web`, `GOOGLE_CLIENT_ID`, and `GOOGLE_CLIENT_SECRET` privately with the public and admin origins. A desktop loopback client cannot be reused as an arbitrary remote-domain callback.

Use separate customer and admin routes at the reverse proxy. The public listener serves only public booking APIs; admin assets and authenticated APIs use the admin listener. Never publish the setup credential in a page or repository. The future Compose template is preparation, not an instruction to replace an existing live deployment.

## References

- [Google OAuth for desktop apps, PKCE and loopback callbacks](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Calendar API permissions](https://developers.google.com/workspace/calendar/api/auth)
- [Google OAuth token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)
- [Google OAuth verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/sensitive-scope-verification)
- [Web-server OAuth and registered HTTPS redirects](https://developers.google.com/identity/protocols/oauth2/web-server)
