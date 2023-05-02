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

See `example-config.yaml` for details about each config option.
