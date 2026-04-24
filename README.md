# Rakhsh

Rakhsh is a Telegram polling bot that receives a URL or Telegram document, downloads it through SOCKS5, stores it locally, and exposes a direct HTTP link.

## What is implemented

- Telegram Bot API polling mode (`getUpdates`) so Telegram never needs to reach your server.
- SOCKS5 proxy support for both:
  - Telegram API and Telegram file downloads
  - External URL downloads
- Resumable downloads with retries (`.part` + `Range` behavior similar to `curl -C -`).
- Simple HTTP server:
  - `GET /healthz`
  - `GET /files/{name}?h={md5}`
- MD5 auth token embedded in stored filename (`name_<md5>.ext`) and also used as query auth token (`h`) so no runtime file re-hash is needed for authorization checks.
- Context-aware shutdown and zap logger usage from context.

## Configuration

Example YAML:

```yaml
http:
  listen: 0.0.0.0:8080
  public_url: https://example.com
  storage: ./data

telegram:
  bot_token: "123456:bot-token"
  # optional fallback if bot_token is empty
  user_token: ""
  allowed_user_ids: [123456789]
  poll_timeout: 30
  update_interval: 10s

proxy:
  socks5_addr: 127.0.0.1:1080
  socks5_user: ""
  socks5_password: ""

download:
  max_file_size: 0
  max_retries: 5
  retry_delay: 2s
```

Notes:
- `telegram.user_token` is supported as a fallback API token when `telegram.bot_token` is empty.
- `download.max_file_size: 0` means unlimited.

## Run

```bash
go run . -c config.yaml
```

Send either:
- a direct `http(s)` URL in text
- a Telegram document attachment

Bot replies with:

```text
File ready: https://example.com/files/file_<md5>.zip?h=<md5>
```
