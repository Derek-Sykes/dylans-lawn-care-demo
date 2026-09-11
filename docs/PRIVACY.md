# Booking application privacy notice

Last updated: September 10, 2026.

X Solutions Booking is self-hosted scheduling software. The operator of each installation controls that installation and its customer records. Downloading this repository does not send customer data to the repository maintainers.

## Information used

The installation operator and invited owners sign in with their own approved Google accounts to use one shared workspace. Only the operator can issue or revoke invitations and remove invited owners' access. The application stores each approved account's stable identifier, email address and access status. Portal sign-in and invitation acceptance request identity permissions only. An approved person explicitly chooses **Connect Google** when the workspace has no account assigned; one consent flow requests Calendar and send-only Gmail permissions and saves the two connections together. The application checks busy times in that account's primary calendar and a dedicated booking calendar, and creates or removes appointments in that dedicated calendar. It does not request access to edit personal events in other calendars.

The assigned Calendar account's send-only Gmail permission supports appointment receipts, confirmations, updates, cancellations and reminders. Email automation remains disabled until an owner enables it. Existing Calendar-only connections require an additional consent through **Enable Gmail** or **Reconnect Google** to add sending permission. The application does not read inbox messages, inspect replies or bounces, or request inbox-reading scopes. Automated messages go to the customer's supplied email address and include appointment details. Private admin notes and customer freeform notes are excluded. Gmail receives the outgoing message and recipient; replies go to the connected Gmail account. See [appointment email behavior](EMAIL_NOTIFICATIONS.md).

Customers provide their name, email address, phone number, property address, selected service, appointment time and optional notes. The owner can add follow-up notes and an appointment status. Appointment details are sent to Google Calendar so the owner can manage the appointment there.

## Storage and access

Booking records, membership, settings and outgoing email records are stored in the installation's local SQLite database. Email records include recipients, message content, timestamps, status and provider references. Imported private Google client configuration, Google access and refresh tokens, and the connected owner identity are encrypted using a key stored with the installation's persistent data; Gmail and Calendar credentials have separate application storage records. The admin interface uses a session cookie tied to an approved identity. Invitation secrets are stored only as hashes; invitation records retain their recipient, expiry and acceptance or revocation status. All approved workspace owners can see the shared customer details, jobs, follow-up notes and recent email activity. Appointment details are also available in the connected account's Google Calendar, not through the public appointment listing. Removing a member blocks their portal access while retaining the shared business records and access history.

For the hosted development installation, “local” means the operator's server: its persistent database volume and protected server configuration. Hosted web OAuth credentials are supplied privately by that operator. The dev environment has separate storage from laptop installations and the existing demonstration website. Its image builds and deployment manifests contain application code and release identifiers, not customer records or personal Google tokens.

Authorized installations can retrieve shared application-level Google OAuth configuration from a private GitHub repository during initial setup. This retrieval uses the operator's existing host Git authentication. The operator may also provision their approved Google identifier and email in that private repository as an access policy. Personal Google tokens, business-owner connections, invitations, customer records and local database contents are not uploaded to GitHub. Subsequent starts use the configuration saved locally.

The software does not include advertising, data sales, marketing email delivery, or analytics tracking. Appointment messages are transactional updates, not a mailing list. It does not use Google user data to train general-purpose AI or machine-learning models. Google user data is used only to provide the scheduling and appointment-notification features the owner authorizes. Any transfer of Google user data is limited to providing those features, as described in the [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy), including its Limited Use requirements.

## Disconnecting and deletion

Owners can turn off automatic appointment emails. The assigned Calendar account holder can also disconnect only the email sender, removing this installation's credential record used for sending without revoking Calendar access. Calendar and email records can contain the same Google tokens from the combined consent, so this local disconnect does not remove Gmail permission from Google's remaining authorization. Pending automatic messages are skipped; a message already submitted to Gmail cannot be withdrawn this way.

The assigned Calendar account holder can disconnect Google from the Calendar panel or revoke the application's access in [Google Account permissions](https://myaccount.google.com/connections). Calendar disconnect removes the installation's Calendar and Gmail grants and stops automatic emails. Google's revocation applies to that user's access for the whole Google project and can affect other installations using the same project. Existing booking records, local email history, sent Gmail messages and calendar appointments are not deleted by disconnecting. Cancel appointments before disconnecting if they should also be removed from Google Calendar.

Source updates and stopping the containers preserve stored data. Routine cleanup removes sent/skipped outgoing email records older than 180 days; failed and uncertain records remain for review. The installation operator controls other retention and backups and is responsible for responding to customer access or deletion requests. Removing the installation's data volume and associated backups deletes locally retained records; calendar events and Gmail messages must be removed separately. An operator should keep records only as long as needed for their business and applicable obligations.

## Contact

For this software's privacy practices, contact **xsolutionsmd@gmail.com**. For customer information held by a particular installation, contact the business that operates that installation. Do not put customer information, account tokens or other secrets in public GitHub issues.

Before offering a hosted customer service, the operator should publish an installation-specific notice with its own contact details and retention practices.
