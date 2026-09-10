# Booking application privacy notice

Last updated: September 10, 2026.

X Solutions Booking is self-hosted scheduling software. The operator of each installation controls that installation and its customer records. Downloading this repository does not send customer data to the repository maintainers.

## Information used

The owner can connect a Google account. The application uses the account's stable identifier and email address to recognize the connected owner. It checks busy times in the primary calendar and a dedicated booking calendar, and creates or removes appointments in that dedicated calendar. It does not request access to edit personal events in other calendars. It does not request Gmail access or read email messages.

Customers provide their name, email address, phone number, property address, selected service, appointment time and optional notes. The owner can add follow-up notes and an appointment status. Appointment details are sent to Google Calendar so the owner can manage the appointment there.

## Storage and access

Booking records and settings are stored in the installation's local SQLite database. Imported private Google client configuration, Google access and refresh tokens, and the connected owner identity are encrypted using a key stored with the installation's persistent data. The admin interface uses a session cookie for authentication. Customer details are available through the authenticated admin interface and the connected owner's Google Calendar, not through the public appointment listing.

For the hosted development installation, “local” means the operator's server: its persistent database volume and protected server configuration. Hosted web OAuth credentials are supplied privately by that operator. The dev environment has separate storage from laptop installations and the existing demonstration website. Its image builds and deployment manifests contain application code and release identifiers, not customer records or personal Google tokens.

Authorized installations can retrieve shared application-level Google OAuth configuration from a private GitHub repository during initial setup. This retrieval uses the operator's existing host Git authentication. It does not upload personal Google tokens, connected-account information, customer records or local database contents to GitHub. Subsequent starts use the configuration saved locally.

The software does not include advertising, data sales, marketing email delivery, or analytics tracking. It does not use Google user data to train general-purpose AI or machine-learning models. Google user data is used only to provide the scheduling features the owner authorizes. Any transfer of Google user data is limited to providing those features, as described in the [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy), including its Limited Use requirements.

## Disconnecting and deletion

The owner can disconnect Google from the admin interface or revoke the application's access in [Google Account permissions](https://myaccount.google.com/connections). Disconnecting removes the installation's stored Google tokens. It does not delete existing booking records or calendar appointments. Cancel appointments before disconnecting if they should also be removed from Google Calendar.

Source updates and stopping the containers preserve stored data. The installation operator controls retention and backups and is responsible for responding to customer access or deletion requests. Removing the installation's data volume and associated backups deletes locally retained records; calendar events must be removed separately. An operator should keep records only as long as needed for their business and applicable obligations.

## Contact

For this software's privacy practices, contact **xsolutionsmd@gmail.com**. For customer information held by a particular installation, contact the business that operates that installation. Do not put customer information, account tokens or other secrets in public GitHub issues.

Before offering a hosted customer service, the operator should publish an installation-specific notice with its own contact details and retention practices.
