# Rakhsh

Rakhsh is a Telegram bot that fetches files from the internet/telegram and makes them accessible via direct hosting links.

## Overview

Rakhsh acts as a lightweight fetcher:

* Accepts a URL/File via Telegram
* Downloads the file
* Stores it on a server
* Returns a public link for access

Designed for simplicity, automation, and minimal user interaction.

## Features

* URL-based file fetching
* Telegram file fetching
* Automatic file hosting
* Direct download links
* Stateless interaction via Telegram
* Suitable for automation workflows

## How It Works

1. User sends a URL/File to the bot
2. Rakhsh downloads the file
3. File is stored on configured storage
4. Bot replies with a public link

As per url generated will require you to pass md5 sum of the file in query parameter, server does not have a authentication system yet.

## Requirements

* Telegram Bot Token
* Telegram User Token (optional)
* Publicly accessible storage endpoint
* Server with outbound internet access (or access to internet via socks5 proxy)
* Optional: reverse proxy for file serving

## Configuration

Environment variables:

```
TELEGRAM_BOT_TOKEN=<your_token>
STORAGE_PATH=<local_or_remote_path>
PUBLIC_BASE_URL=<https://your-domain/files>
MAX_FILE_SIZE=<bytes>
```

## Usage

Start the bot and send a message:

```
https://example.com/file.zip
```

Response:

```
File ready: https://your-domain/files/abc123.zip
```

## Deployment

Typical setup:

* Bot service (Go/Rust recommended)
* File storage (local disk, S3-compatible, or NFS)
* HTTP server (nginx, caddy) to expose files

## Security Considerations

* Enforce file size limits
* Validate URLs and content types
* Prevent SSRF by restricting internal IP ranges
* Optionally require authentication or allowlist users

## Limitations

* Large files depend on available bandwidth and storage
* No built-in deduplication
* No resumable downloads by default

## Future Improvements

* Queue system for large downloads
* Deduplication and caching
* Expiration policies for hosted files
* Parallel downloads
* Web UI for management

## License

MIT
