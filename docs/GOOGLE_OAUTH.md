# Google sign-in, Calendar and appointment email

The local application uses a Google **Desktop app** OAuth client and a loopback callback at `http://127.0.0.1:<admin-port>/oauth/callback`. The launcher chooses the port, so installations do not need a fixed port or a registered callback for every laptop.

`booking/config/google-client.json` contains the app's public client identifier. It grants no access to a Google account by itself. Account access requires the owner's consent and the application's PKCE verifier. Private credentials must never enter this public repository or its images. Browser sessions and personal Google tokens stay in each installation's local data volume.

## First connection on a new computer

Run `./website.ps1 start` in PowerShell or `./website start` in Bash. After the containers become healthy, the launcher checks Google application configuration and operator access independently. It retrieves whichever is missing from `google-client.json` and `operator-access.json` on the `dev` branch of the private `Derek-Sykes/xsolutions-booking-private` repository using the host's existing Git authentication. The launcher passes each needed file through standard input to the booking container, which validates and encrypts it in the persistent local database. The temporary fetch is removed before startup finishes.

The approved operator signs in with Google, then invites the people who need the shared workspace from **Access & invitations**. Each recipient signs in with their own invited Google account; this does not assign or connect a calendar. When the workspace has no calendar yet, an approved person explicitly chooses **Connect Google**, approving the two Calendar permissions and send-only Gmail permission in one consent flow. That connection assigns the shared calendar account and saves both Calendar and email credentials together. Email automation remains disabled until enabled under **Emails**. An operator can instead connect their own Google account on an isolated testing installation. No manual JSON import or environment editing is needed on an authorized laptop. `dev` and `update` use the same setup check. Existing configured installations do not fetch private configuration again; normal source updates preserve it alongside workspace membership, the connections and customer records.

The Git account must have read access to the private repository. Existing Git/Git Credential Manager authentication is reused; being signed into GitHub in a browser alone does not establish that authentication. The public source remains clonable by anyone, but private automatic setup is available only to authorized accounts. The launchers do not install credential helpers, change global Git authentication, or forward GitHub credentials into the booking container. A missing file, denied access or invalid configuration stops startup before browser opening, with a safe error. Local runtime data is preserved.

## One-time operator provisioning

Keep `Derek-Sykes/xsolutions-booking-private` private. Add the actual desktop OAuth configuration as `google-client.json` at the root of its `dev` branch, and add the approved operator identity as `operator-access.json` in that same private branch. Grant read access only to the GitHub accounts permitted to initialize installations. See [operator provisioning and invitations](ADMIN_ACCESS.md). This is central operator setup, not a step repeated on each laptop.

The file can use Google's downloaded desktop-client format (`installed.client_id` and `installed.client_secret`) or a JSON object with `client_id` and `client_secret`. It must be no larger than 64 KiB, and its identifier must match the public client configured by the application. Do not store personal Google refresh tokens, customer information, local bootstrap credentials, databases or GitHub authentication tokens in that repository. Do not put the private configuration under this public project's `dist/` or `booking/` directories.

The registered Google endpoint requires the desktop client secret. On September 10, 2026, both a token-endpoint preflight and the complete browser consent flow rejected a PKCE exchange without it with `client_secret is missing`. Automatic private retrieval supplies that required application configuration without exposing it in the public source. Supporting arbitrary public cloners without granting private repository access would require a separate distribution or OAuth service; none is deployed by this project.

The encrypted configuration is never returned by the admin API. The operator's import/replace controls remain available for recovery or rotation, along with the initial setup recovery path on an unconfigured installation. Invited owners cannot change application credentials. `GOOGLE_CLIENT_SECRET` supplied privately through the environment or ignored `.env` takes precedence over stored configuration. Copying only the public source to a different computer creates a new installation: it retrieves application configuration through authorized Git and asks for that installation's own Google consent.

## Permissions and calendar behavior

Portal sign-in and invitation acceptance request only `openid` and `email`. The workspace's explicit **Connect Google** action requests these permissions together in one consent flow:

- `openid` and `email` to recognize the connected owner.
- `https://www.googleapis.com/auth/calendar.freebusy` to check busy times.
- `https://www.googleapis.com/auth/calendar.app.created` to manage its dedicated booking calendar.
- `https://www.googleapis.com/auth/gmail.send` to send appointment email from the same account.

Appointments appear in a separate booking calendar within the owner's Google Calendar. Availability checks include the owner's primary calendar and the dedicated booking calendar. Other secondary calendars are not included in this first version. The application cannot edit personal events in the primary calendar. Ordinary portal sign-in does not request Calendar or Gmail access.

The combined connection requires both Calendar permissions and Gmail sending permission before saving either connection, and verifies that the Google identity matches the account allowed to connect. Calendar and email credentials are saved atomically in separate encrypted application records. Those records can contain the same tokens issued by the combined Google consent; separate storage does not make the permissions independently revocable at Google. The email sender always belongs to the assigned Calendar account. Other workspace owners can manage appointments without granting access to their own Gmail.

An existing Calendar-only installation continues to work without silently gaining Gmail access. Its assigned account holder chooses **Enable Gmail** under **Emails** for one additional consent. **Reconnect Google** appears only when Calendar needs attention and then uses the combined connection flow. The additional email flow requests `openid email` plus `https://www.googleapis.com/auth/gmail.send` and preserves the existing Calendar connection. Sending permission does not allow reading the inbox, inspecting received messages or accessing Gmail profile/history APIs. Email automation stays off until an owner enables it; consent alone does not email existing bookings. See [appointment email behavior](EMAIL_NOTIFICATIONS.md) and [Google's Gmail scope definitions](https://developers.google.com/workspace/gmail/api/auth/scopes).

Disconnecting only the email sender removes the local credential record used for sending and disables automation while leaving Calendar working. It does not revoke Google's combined permission: the remaining Calendar credential can still carry the Gmail scope, which the Calendar code does not use. Disconnecting Google from the Calendar panel removes both local credential records and revokes the user's authorization for the whole Google project, potentially affecting other installations using that project. See [disconnecting and privacy](PRIVACY.md#disconnecting-and-deletion).

Working hours define the appointment window. Weekly or date-specific time off, Google busy events and existing reservations remove times from that window. Manual availability never overrides a Calendar conflict. Free intervals are calculated before appointment starts are generated. External Calendar events use a separate configurable 30-minute buffer; known customer bookings use their saved business-appointment gap (15 minutes by default). The application checks Google again before saving a request, but Google has no atomic reservation operation: an owner could still create a conflicting personal event at exactly the same moment. Time and duration edits in the dedicated booking calendar are synchronized into the portal and availability, normally within a minute. Google deletions cancel matching bookings. Customer follow-up status remains separate from Calendar synchronization.

Private configuration approves the operator identity. Each expiring invitation adds a verified Google identity to the shared workspace. Before a calendar is assigned, any approved owner or operator may explicitly connect their own account. Completing that connection assigns Calendar ownership; invitations and ordinary sign-ins do not. The assigned calendar account can disconnect and reconnect the same account. Other workspace members can manage shared bookings without authorizing Calendar for their own accounts. The dedicated calendar identifier is preserved so reconnecting does not intentionally create another calendar.

Tokens and the owner identity are encrypted in the local data volume. Updates preserve that volume. Google can still revoke or expire a grant; in that case the application asks the owner to reconnect and stops accepting appointments until calendar access is restored. Keeping files through an update cannot override Google's grant-expiration rules.

## App registration

The maintained client belongs to the `xsolutions-booking` Google Cloud project, with the Calendar API enabled. This registration is separate from other applications. The [privacy notice](PRIVACY.md) describes the software's data handling.

Google's registration status is independent of a website deployment. The September 10, 2026 check found an **External / In production** audience with four non-sensitive identity/Calendar scopes and no required data-access verification for those scopes. That finding predates appointment email: **`gmail.send` is a sensitive scope**, so it must be declared in the Google project's consent configuration and the Gmail API must be enabled. An audience setting of Production does not mean the new Gmail scope has been verified. Branding verification is also separate.

Google allows development/testing/staging use without completed verification, subject to an unverified-app warning and applicable user limits. Unverified sensitive-scope access normally has a lifetime cap of 100 new users; Google Workspace administrators can impose additional restrictions. Plan the production registration and verification separately before broad customer onboarding. A successful development consent flow is evidence that a tested account can authorize the feature, not approval for unrestricted distribution. See [when verification is not needed](https://support.google.com/cloud/answer/13464323?hl=en) and [Google's app audience limits](https://support.google.com/cloud/answer/15549945?hl=en).

If the app is changed back to **Testing**, Calendar/Gmail consent is limited to registered test accounts and its refresh grants normally expire after seven days. Basic identity-only sign-in (`openid email`) is an exception to the test-account allowlist; it still requires an invitation or approved installation identity to access this workspace. Google Workspace administrators may apply their own account restrictions. See [Google's OAuth application states](https://developers.google.com/identity/protocols/oauth2/production-readiness/overview?hl=en). The completed validation record documents the actual real sign-in result for each release.

## Hosted development and future installations

The manually released dev environment uses a separate **X Solutions Booking Dev Server** web client in the same Google project, registered September 10, 2026. Its callback is `https://dev-demo.xsolutionsmd.com/oauth/callback`; its owner portal is `/admin/`. The private web client secret is stored only in that server's protected runtime configuration, separate from the desktop configuration repository. The dev volume keeps its own owner, tokens and calendar connection across manual deployments. See [manual deployment](DEV_DEPLOYMENT.md).

A remotely hosted admin domain needs a separate **Web application** OAuth client with its exact HTTPS callback registered, for example `https://admin.example.com/oauth/callback`. Configure `GOOGLE_OAUTH_MODE=web`, `GOOGLE_CLIENT_ID`, and `GOOGLE_CLIENT_SECRET` privately with the public and admin origins. A desktop loopback client cannot be reused as an arbitrary remote-domain callback.

Use separate customer and admin routes at the reverse proxy. The public listener serves only public booking APIs; admin assets and authenticated APIs use the admin listener. For a shared HTTPS hostname, set `ADMIN_BASE_PATH=/admin` and route the owner paths accordingly. Separate origins and the desktop loopback behavior remain supported. Never publish the setup credential in a page or repository. The future production Compose template is preparation, not an instruction to replace an existing live deployment.

## References

- [Google OAuth for desktop apps, PKCE and loopback callbacks](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Calendar API permissions](https://developers.google.com/workspace/calendar/api/auth)
- [Google OAuth token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)
- [Google OAuth verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/sensitive-scope-verification)
- [Web-server OAuth and registered HTTPS redirects](https://developers.google.com/identity/protocols/oauth2/web-server)
