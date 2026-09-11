# Operator and business-owner access

The portal has two approved Google identities: an installation operator and an invited business owner. Both can use the appointment lists and availability settings. Only the operator can create or revoke an owner invitation. There is no public admin registration.

## Normal use

1. The operator opens the admin address and chooses **Sign in with Google**. The installation checks the verified Google identity against its private operator configuration.
2. In **Access**, the operator enters the owner's Google email address and creates an invitation. It expires after 24 hours. Copy the generated link and send it privately; the application does not send email or messages.
3. The recipient opens the link and signs in with that Google account. The invitation is email-bound, expires and can be redeemed only once. Opening the URL alone does not accept it.
4. Once signed in, the owner connects Google Calendar. Subsequent portal sign-ins identify the owner without asking for Calendar consent again. Calendar permission only needs renewing when Google requires it or the owner disconnects it.

The operator retains access to the same business workspace. Signing in as the operator does not replace the owner's connected calendar with the operator's calendar.

The invite token is in a URL fragment, removed immediately by the page and sent only in the acceptance request. Only a hash is retained in the database. Link creation displays the full link once. Revocation and expiry are checked again when Google returns, so a link cannot be accepted after being revoked while sign-in was in progress. Generating a replacement supersedes the previous unused invitation.

## Private installation policy

The private configuration repository's **dev** branch contains application configuration in `google-client.json` and the approved operator identity in `operator-access.json`. The identity file is limited to:

```json
{
  "schema": 1,
  "email": "operator@example.com"
}
```

This file identifies who is allowed to sign in. On the first successful Google sign-in with that verified email, the application atomically binds Google’s permanent account identifier. Future sign-ins must match that identifier. An optional `googleSub` can pre-pin a known verified identifier; importing the email-only file later never removes an existing binding. It contains no password, session, invitation, Calendar ID or Google access/refresh token. Possessing it does not prove that a visitor owns that Google account; the visitor must complete Google authentication. Never add credential fields to it; the importer rejects unknown fields.

Windows and Bash launchers retrieve missing application and operator configuration through the host's existing private Git access, then encrypt both in the installation's persistent volume. Once present, normal starts and updates work without fetching the private repository again. Imports never replace a different existing operator. A fresh installation with the policy in place opens the ordinary Google sign-in page.

The booking executable supports host-side provisioning through standard input:

```sh
booking operator-config status
booking operator-config import < /private/operator-access.json
booking operator-config export > /private/operator-access.json
```

These commands run inside the configured booking container. Status returns 0 when configured and 3 when absent. Export writes only identity data; on an older installation, an explicitly requested export can derive that identity from its previously verified Google owner. Export does not grant that owner operator access. Import is the explicit provisioning step. Protect exported files and never paste their contents into public issues.

Hosted deployments can instead supply `OPERATOR_GOOGLE_EMAIL` and optionally `OPERATOR_GOOGLE_SUB` in protected server configuration. The same identity is then saved privately on that installation. A conflicting identity is refused. The future production Compose template requires this policy; it is not selected by the existing static main release.

## Existing testing installations and production

Existing Calendar tokens, settings and booking records stay in their current volume. Old sessions without an identified actor are invalidated: sign in again with Google. Once an operator is configured or a Google owner exists, the old reusable bootstrap URL no longer grants admin access. The original setup credential is kept only to recognize the installation and for the unconfigured initial setup path.

An invitation cannot silently replace a different person's connected calendar or transfer existing bookings. Use a fresh dedicated production volume for the business owner, provision the operator identity, and invite the owner before connecting a test calendar. A development calendar is not a production handoff mechanism.

Production still needs a separately authorized backend deployment, its registered HTTPS OAuth callback and private application configuration. Do not copy a development database, personal Calendar tokens or test bookings to initialize production. Main's currently deployed static website remains independent of this dev work.

## Storage and recovery

Operator and owner identities, sessions, invitation records and encrypted Google tokens belong to each installation's persistent database. Updates preserve that volume. Generated invitation links do not belong in GitHub, even a private repository: issue a fresh short-lived link from the operator portal when needed.

Keep the database and encryption key together in a protected backup. Server administrators control the host and therefore can administer its private data; application roles do not protect against a compromised server administrator. Recovery or replacement of an established operator requires a deliberate server-side process, not a publicly reusable setup link.
