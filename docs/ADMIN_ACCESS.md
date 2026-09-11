# Shared workspace access

The portal supports an installation operator and multiple invited owners in one shared workspace. Everyone sees the same appointments, estimates, customer details and availability settings. Only the operator can invite people or remove their access. There is no public admin registration, separate workspace per owner, or extra container per person.

## Normal use

1. The operator opens the admin address and chooses **Sign in with Google**. The installation checks the verified Google identity against its private operator configuration.
2. In **Access & invitations**, the operator enters a person's Google email address and creates an invitation. It expires after 24 hours. Copy the generated link and share it privately; invitation links are shared manually. Appointment emails are a separate feature under **Emails**. Repeat for each person you want to add.
3. The recipient opens the link and signs in with that Google account. The invitation is email-bound, expires and can be redeemed only once. Opening the URL alone does not accept it.
4. The recipient can immediately manage the same bookings and settings as the other owners. They do not need to connect their own calendar or access the private GitHub repository.
5. If the workspace has no calendar yet, an approved person opens **Google Calendar** and explicitly chooses **Connect Google**. One consent flow requests the Calendar permissions and send-only Gmail permission for that account, then saves both connections together. This assigns the shared calendar account and email sender; email automation remains off until enabled under **Emails**. Joining through an invitation never assigns calendar ownership or authorizes Gmail.

One connected Google account supplies the shared booking calendar, busy-time checks and email sender. Everyone manages its bookings through the portal; each person does not contribute a separate calendar. The connected account holder can reconnect Google or disconnect it, and can disconnect only the email sender under **Emails** while keeping Calendar working. An older Calendar-only connection needs one additional consent through **Enable Gmail** or **Reconnect Google** to add email. Other owners' portal sign-ins and invitations never replace this account. Inviting another owner does not grant them direct access in Google's own Calendar interface.

The invite token is in a URL fragment, removed immediately by the page and sent only in the acceptance request. Only a hash is retained in the database. Link creation displays the full link once. Revocation and expiry are checked again when Google returns, so a link cannot be accepted after being revoked while sign-in was in progress. Invitations to different people stay valid independently. Generating a replacement for the same email supersedes only that email's previous unused invitation.

## People with access

The operator can see the current people under **Access & invitations**. Removing an invited owner requires an inline confirmation and blocks their portal sessions; it preserves the shared bookings, availability and calendar. They need a new invitation and a new sign-in to regain access. Re-adding them does not revive an older revoked session. The operator and the assigned calendar account are protected from removal here, so the workspace cannot lose its access manager or calendar through an accidental member removal.

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

Existing operators and calendar accounts retain access on upgrade. Additional owners can join that same installation without a calendar handoff. Membership and calendar ownership are separate: invitations never replace a connected account or move bookings. For a new production workspace, use a fresh dedicated volume, provision the operator, invite the people who need access, and let the intended calendar account explicitly connect. A development calendar is not a production handoff mechanism.

Production still needs a separately authorized backend deployment, its registered HTTPS OAuth callback and private application configuration. Do not copy a development database, personal Calendar tokens or test bookings to initialize production. Main's currently deployed static website remains independent of this dev work.

## Storage and recovery

Operator and member identities, access status, sessions, invitation records and encrypted Google tokens belong to each installation's persistent database. Updates preserve that volume. Generated invitation links do not belong in GitHub, even a private repository: issue a fresh short-lived link from the operator portal when needed.

Keep the database and encryption key together in a protected backup. Server administrators control the host and therefore can administer its private data; application roles do not protect against a compromised server administrator. Recovery or replacement of an established operator requires a deliberate server-side process, not a publicly reusable setup link.
