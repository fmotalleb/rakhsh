# Rakhsh

Rakhsh fetches files sent through Telegram or plain URLs, stores them locally, and serves authenticated direct links.

## Modes

- `telegram.mode: bot_api` (default)
  - Uses Telegram Bot API polling (`getUpdates`).
  - Supports URL + document/photo/video/audio/voice/video-note from bot chats.
  - Optional MTProto user fallback for large Telegram media when Bot API cannot fetch the file.
- `telegram.mode: mtproto_user`
  - Uses a real Telegram user account through MTProto (`gotd/td`) with `api_id` + `api_hash` + session storage.

## Large File Fallback (Bot + User)

If you keep `bot_api` mode and set `telegram.mtproto.fallback_enabled: true`, Rakhsh will:
1. Try Bot API download first.
2. If Bot API media fetch fails, try MTProto user download for the same chat/message.
3. If `telegram.mtproto.fallback_forward_chat_id` is set, bot first forwards the message there, then MTProto downloads from forwarded message.
4. If sender is the same as configured MTProto user, bot skips forwarding and uses direct MTProto fetch path.

Requirements for fallback to work:
- MTProto account must be authorized (`api_id`, `api_hash`, session).
- MTProto account must have access to the source chat/message, or you must set `fallback_forward_chat_id` to a chat the MTProto user account can access.

Recommended setup:
- Start your bot from the same Telegram user account used for MTProto.
- Send `/ids` to the bot from that account and use returned `chat_id` as `fallback_forward_chat_id`.
- This makes bot relay large-media messages to that chat, then MTProto can fetch reliably.

## Features

- SOCKS5 proxy support for outbound HTTP downloads (and Bot API HTTP requests in bot mode).
- Resumable URL downloads (`.part` + `Range`, similar to `curl -C -`).
- Direct file hosting via built-in HTTP server.
- Link auth based on embedded MD5 suffix in filename + `h` query.
- Live progress updates by editing one status message (`telegram.update_interval`, default `2s`).
- Context-aware shutdown and zap logger from context.

## Configuration

See [config.yaml.example](config.yaml.example).

Important for MTProto:
- `telegram.mtproto.api_id` and `telegram.mtproto.api_hash` are required.
- `telegram.mtproto.session_file` stores your user session.
- If session is not authorized yet, set `phone` + one-time `auth_code` (and `password` if 2FA is enabled) for initial login.

### How to receive required MTProto info

1. `api_id` and `api_hash`
- Go to `https://my.telegram.org/apps`.
- Create an app and copy `api_id` and `api_hash`.

2. `phone`
- Your Telegram account phone in international format (example: `+98912...`).

3. `auth_code`
- On first run without an existing authorized session, Telegram sends a login code to your Telegram app/device.
- Put that code in `telegram.mtproto.auth_code` and run once.
- After successful login and session creation, you can clear `auth_code`.

4. `password` (optional)
- Only if your Telegram account has 2FA enabled.

5. `fallback_forward_chat_id` (for relay fallback)
- Send `/ids` to your bot from the relay chat.
- Use `chat_id` from the response.
- You can also use `/ids` to capture `from_user_id` for `allowed_user_ids`.

## Run

```bash
go run . -c config.yaml
```

The bot/userbot replies with links like:

```text
File ready: https://example.com/files/file_<md5>.zip?h=<md5>
```
