# Appointment email

Appointment emails use the assigned Calendar account's Gmail. The workspace's explicit **Connect Google** action requests Calendar and send-only Gmail permissions together in one consent flow. This feature belongs to the development booking application. The static main/demo website and its release workflow are unchanged.

## Set up the sender

1. Sign into the admin workspace. For a fresh workspace, the intended calendar account holder chooses **Connect Google** under **Google Calendar** and approves both Calendar permissions and sending email. The application saves the shared calendar connection and email sender together. Ordinary portal sign-in and invitation acceptance request identity permissions only.
2. Open **Emails**. A new combined connection is already ready here. If an older installation has Calendar connected without Gmail permission, that account holder chooses **Enable Gmail** here and approves the additional permission once. **Reconnect Google** appears in the Calendar panel only when Calendar needs attention; that recovery flow also requests both permissions.
3. Use **Send test email** to send a message to the connected sender account. The test cannot target an arbitrary address and does not email a customer.
4. Enable **Automatic appointment emails**, choose the reminder preference, and save. Automation starts disabled. The default reminder preference is one reminder 24 hours before a confirmed appointment; the available choices are 1, 2, 6, 12, 24 or 48 hours.

All approved workspace owners can view recent email activity and change the shared notification preferences. Only the assigned Calendar account holder can connect or disconnect the sender. A person joining the workspace does not authorize their own Gmail and does not replace the sender. Workspace invitation links still need to be shared separately; appointment-email setup does not automatically email invitations.

The operator enables the Gmail API and declares `https://www.googleapis.com/auth/gmail.send` in the registered Google project's consent configuration. This sensitive scope has separate Google verification requirements from the existing identity/Calendar scopes; see [Google OAuth setup](GOOGLE_OAUTH.md). Gmail or Google Workspace account restrictions and sending limits still apply.

## What customers receive

| Event | Message |
| --- | --- |
| A new service or estimate/callback request | A receipt saying the requested time still needs confirmation. |
| An owner marks the request Confirmed | A confirmation with the appointment details. |
| A future appointment's time changes | An updated time, retaining a needs-confirmation explanation when appropriate. |
| A future appointment is cancelled | A cancellation notice. |
| A confirmed appointment reaches its reminder time | One reminder for the current scheduled appointment. |

Messages include the service, property address, local appointment date/time and business name. Estimate/callback messages identify that purpose clearly. Customer freeform notes and private admin notes are excluded. Replies go to the connected Gmail account. A Calendar reservation and customer confirmation remain separate states.

Updating the software or enabling emails does **not** scan old bookings and send a backlog. Automatic Calendar synchronization cannot enroll an older booking merely because email was enabled. A later explicit booking/status action can start notifications for that booking. Repeating an unchanged action or an idempotent customer submission does not queue another message.

Changing or cancelling an appointment supersedes queued messages about its previous state. Confirmations and reminders wait for Calendar synchronization. Reminders also require a confirmed future appointment and a current Calendar check; a temporary Calendar problem postpones the attempt. If confirmation comes after the selected reminder window, the confirmation serves as the notification; no immediate extra reminder is added. Changing the reminder preference retimes only reminders already queued and does not recreate skipped reminders.

## Delivery status and retry

The application saves an outgoing record in the same database transaction as the booking change. Its existing background worker processes the outbox; no extra container, Redis service or external job scheduler is needed. The outbox and preferences survive updates and restarts with the persistent database volume. The **Recent emails** view shows the latest 100 records. Routine cleanup removes sent/skipped email records older than 180 days; failed and uncertain records remain for review. This cleanup does not delete appointments or messages retained by Gmail.

- **Queued:** saved for an upcoming attempt or reminder time.
- **Sending:** an attempt has started.
- **Sent via Gmail:** Gmail returned an acceptance reference. This does not prove delivery to the recipient's inbox.
- **Failed:** sending was rejected or could not begin; review the connection and retry when appropriate.
- **Uncertain:** the application cannot tell whether Gmail accepted the message. Check the sender's Sent folder before confirming a manual retry, which could duplicate a message.
- **Skipped:** the appointment or preferences changed, or the message is no longer relevant.

Explicit temporary rejections and failures before submission use bounded retries. A lost response, ambiguous server error, or interrupted attempt is not automatically resent. A stable email Message-ID helps identify an attempt, but is not treated as a provider guarantee against duplicates. Manual retry rechecks the current appointment and preferences; stale messages cannot be resent through this control. Gmail's own guidance warns that even a successful API response is not proof of successful final delivery. See [Gmail sending errors and limits](https://developers.google.com/workspace/gmail/api/guides/handle-errors#mail_sending_limits).

Once a send has started, changing a booking or disabling emails cannot retract a message already submitted to Gmail. The history keeps its result rather than claiming the recipient's copy was withdrawn.

## Disconnecting and privacy

Turning automatic emails off skips pending automatic messages. Disconnecting the email sender also removes this installation's encrypted credential record used for sending and invalidates pending consent that would reconnect email. It leaves Calendar connected and does not revoke the Google project's entire authorization.

Disconnecting Google from the Calendar panel removes both local credential records and stops automatic emails. Google's revoke operation applies to the user's authorization for the whole Google project, including other OAuth clients under that project; another installation using the same Google account/project may need to reconnect. Existing portal accounts, appointments and history remain. Reconnecting Gmail does not automatically restore discarded outgoing messages. See [Google token revocation](https://developers.google.com/identity/protocols/oauth2/web-server#tokenrevoke).

The app requests send-only Gmail access. It does not read the inbox, inspect delivery/bounce messages, or use inbox-reading permissions to reconcile uncertain sends. Gmail receives the recipient and message content, and sent messages remain subject to the account's normal Gmail behavior. Locally stored message bodies, recipients, status and provider references are part of the installation's private business records. Calendar and email credentials have separate encrypted storage records but can contain the same Google tokens from the combined consent. Disconnecting only email removes the record the sending code uses; it does not remove Gmail permission from the remaining Google authorization. Complete volume backups must include the matching encryption key. See [privacy](PRIVACY.md) and [backup operations](LOCAL_OPERATIONS.md).

## Validation scope

Automated checks use isolated databases and mocked Google/Gmail endpoints. They exercise sender identity, scope separation, MIME encoding and header injection, OAuth state/browser binding/replay, expired permissions, durable queue behavior, calendar changes, reminders and uncertain retries. These tests do not send real customer email. A separately authorized real Gmail test establishes only the observed result for its stated recipient; record that result in the release validation notes.
