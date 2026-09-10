# Google Calendar connection

The local application uses a Google **Desktop app** OAuth client and a loopback callback at `http://127.0.0.1:<admin-port>/oauth/callback`. The launcher chooses the port, so installations do not need a fixed port or a registered callback for every laptop.

`booking/config/google-client.json` contains the app's public client identifier. It grants no access to a Google account by itself. Account access requires the owner's consent and the application's PKCE verifier. Private credentials, browser sessions and Google tokens must never be committed.

## First connection on a new computer

Start with the normal PowerShell or Bash command. In the owner portal's Google Calendar section, import the private Google client JSON supplied separately by the application operator, then choose **Connect Google Calendar** and approve the two Calendar permissions. This is a one-time configuration import for that installation. The JSON is not included in the public repository.

The import accepts Google's downloaded desktop-client format (`installed.client_id` and `installed.client_secret`) or a JSON object with `client_id` and `client_secret`. The identifier must match the application's configured public client. The private value is stored encrypted in the persistent local database and is never returned by the admin API. Do not upload this JSON to GitHub or put it under `dist/` or `booking/`.

This extra step is required by the actual Google endpoint for the registered client. On September 10, 2026, both a token-endpoint preflight and the complete browser consent flow rejected a PKCE exchange without a secret with `client_secret is missing`. Google's desktop documentation lists that field as optional, but this client did not work without it. A fresh public clone therefore cannot honestly promise Google sign-in with no private configuration. Providing that experience would require a separately hosted OAuth service; none is deployed by this project.

The operator can instead supply `GOOGLE_CLIENT_SECRET` privately through the environment or ignored `.env`. Environment configuration takes precedence over an imported value. All source updates preserve the imported database configuration; copying only the public source to a different computer creates a new installation that needs its own import and Google consent.

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
