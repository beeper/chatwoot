# Chatwoot <-> Matrix Help Bot Integration

A bot which logs in as a help user in Matrix and mirrors all messages sent to it
from Matrix into Chatwoot and allows Chatwoot agents to respond.

Matrix chat:
[#chatwoot-matrix:nevarro.space](https://matrix.to/#/#chatwoot-matrix:nevarro.space)

## Supported features

- [x] Chatwoot -> Matrix

  - [x] Plain text
  - [ ] Message formatting
  - [x] Attachments
    - [x] Images
    - [x] Files
  - [x] Private messages are ignored
  - [x] Redactions
  - [x] Append message sender to message that gets mirrored into Matrix

- [x] Matrix -> Chatwoot

  - [x] Text
  - [x] Message formatting
  - [x] Attachments
    - [x] Images/GIFs
    - [x] Files
  - [x] Edits \*
  - [x] Reactions \*
  - [x] Redactions
  - [x] Mark the canonical DM with a label

- [x] Multiple chats with help bot supported
- [x] Read receipt sent when message is sent from Chatwoot or when conversation
      is resolved in Chatwoot
- [x] Error notifications as private messages when bridging fails in either
      direction

\* indicates that a textual representation is used because Chatwoot does not
support the feature

## Configuration

The `config.json` file can have the following keys. Required fields are marked
with a \*.

**Matrix Authentication**

* `Homeserver` \* --- the Matrix homeserver to connect to
* `Username` \* --- the Matrix username of the help bot
* `PasswordFile` \* --- a file containing the Matrix user password

**Chatwoot Authentication**

* `ChatwootBaseUrl` \* --- the base URL for the Chatwoot instance
* `ChatwootAccessTokenFile` \* --- a file containing the access token for
  Chatwoot
* `ChatwootAccountID` \* --- the Chatwoot account ID to use
* `ChatwootInboxID` \* --- the Chatwoot inbox ID to create conversations in

**Database Settings**

* `DBConnectionString` \* --- a PostgreSQL database connection string for the
  bot

**Bot Settings**

* `AllowMessagesFromUsersOnOtherHomeservers` --- `true` or `false` indicating
  whether or not to create conversations for messages originating from users on
  other homeservers. Defaults to `false`.
* `CanonicalDMPrefix` --- if not `""`, when creating a conversation, if the
  Matrix room name starts with this prefix, it will be labeled with the
  `canonical-dm` label. Defaults to `""`.
* `BridgeIfMembersLessThan` --- if not `-1`, only bridge conversations where the
  member count in the room is less than this. Defaults to `-1`.
* `RenderMarkdown` --- `true` or `false` indicating whether or not to convert
  the Chatwoot markdown to Matrix HTML.

**Webhook Listener Settings**

* `ListenPort` \* --- the port to listen for webhook events on
