# Google desktop client

The application can start without Google configuration; booking then stays unavailable.
The project operator supplies `google-client.json` with a public `client_id` (or an
`installed.client_id` field). Never place refresh tokens, user information, or
client secrets here. `GOOGLE_CLIENT_ID` overrides this identifier.

Google requires private client configuration for the registered desktop client.
The website launchers retrieve missing configuration from the authorized private
GitHub repository and stream it into `booking google-config import`. The CLI stores
it encrypted in the installation's persistent data volume. The imported client ID
must match this installation's public identifier. The secret is never returned
through the API. Private `GOOGLE_CLIENT_SECRET` environment configuration takes
precedence when supplied. The authenticated owner portal also supports replacement
for recovery. Keep the private file outside this directory and this public repository.

The local desktop client uses a loopback callback at the actual `ADMIN_ORIGIN`
port. A future hosted installation must select web mode and separately configure
its HTTPS callback and confidential client credentials.
