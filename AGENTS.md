# AGENTS.md

Rakhsh: a Telegram bot (Go 1.26, single module `github.com/fmotalleb/rakhsh`) that fetches files from Telegram/URLs and exposes them via secret HTTP download links. Entrypoint: `main.go` → `cmd` (cobra) → `app.Run`.

## Developer commands

- `make precommit` — full validation gate (`mdlint mod gen build spell lint test vuln`). Run before committing. Note: `lint` runs golangci-lint with `--fix`, `spell` auto-rewrites markdown, `mod` runs `go mod tidy`, and `build` is GoReleaser (`go tool goreleaser build --clean --single-target --snapshot`), NOT `go build`. All may modify files — review `git diff` after.
- `make ci` — `precommit` + a dirty-tree check (`git diff --exit-code`). This is what CI runs.
- `make lint` — `go tool golangci-lint run --fix` (auto-edits).
- `make test` — `go test -race` + coverage.html. `-race` is skipped when `CGO_ENABLED=0`.
- `make vuln` — `govulncheck ./...`.
- `make mdlint` — dockerized markdownlint; set `SKIP_DOCKER=1` to skip (no docker, or macOS/Windows CI).
- `make run` — `go run .`. `make gen` — `go generate ./...` (currently a no-op; no `go:generate` directives exist).

## Running

- `go run . -c config.yaml` runs the bot.
- `go run . mtproto-session` interactively creates/refreshes the MTProto session file.
- Without `-c`, config is read from **stdin**.

## Config

- Structs + defaults in `config/config.go`; yaml/mapstructure tags. Decode hooks in `config/hooks.go` (string→`netip.AddrPort`, `net.IP`, `net.IPNet`; a bare port string defaults host to `127.0.0.1`).
- Parsed via `github.com/fmotalleb/go-tools/config` `ReadAndMergeConfig`: accepts file paths, glob patterns, http(s) URLs, and deep-merges via an `include` field.
- `config.yaml` is **gitignored** and holds live credentials (`bot_token`, MTProto `api_id`/`api_hash`). Never commit or log it; edit `config.yaml.example` for template changes. `secrets/` and `data/` are also gitignored.

## Architecture

- `app/app.go` wires: `netx` (SOCKS5 → HTTP client) → `downloader` (resumable `.part` files, retries, `max_file_size`) → `storage` (final files named `<name>_<md5>.<ext>`, MD5 computed over content) → `httpserver` + `telegram`.
- Two modes: `bot_api` (default, long-polling) and `mtproto_user` (userbot). In `bot_api` mode an optional MTProto fallback handles large-file downloads (`internal/telegram/mtproto_fallback.go`).
- `internal/telegram/bot.go` handles commands `/ids`, `/whoami`, and a cancel button via `cancel:` callback data.
- `internal/httpserver`: `/healthz` and `/files/`; a file is served only when the MD5 embedded in the filename matches the `?h=` query param (`storage.ExtractHashFromName` + `serveFile`).

## Style / lint

- golangci-lint v2 (`.golangci.yml`): gofumpt + goimports with local-prefix `github.com/fmotalleb/rakhsh` — keep a separate import block for local `github.com/fmotalleb/rakhsh/...` imports.
- `funlen` caps at 100 lines / 50 statements; `nolint` requires an explanation + specific rule; godot-style doc comments expected on exported symbols.
- No test files currently exist; `make test` is effectively a compile check. Match existing code style in `internal/telegram/` (manual Telegram API client, no third-party bot framework).

## Release / docs

- Releases trigger on `v*` tags via goreleaser → GitHub releases + `ghcr.io/fmotalleb/rakhsh` images. `CHANGELOG.md` is maintained manually (Keep a Changelog format, Conventional Commit messages like `feat:`/`fix:`/`chore:`).
- Docker image is based on `scratch`, so containers need CA certs mounted (see `docker-compose.yaml`).

## Workflow requirements

- Review changes with `git diff` (and stage only intended files) before committing; commit after applying changes.
- Public API changes (config schema, CLI commands, link format, HTTP endpoints) require updating `README.md`, `README_FA.md` (Persian mirror), `CHANGELOG.md`, and `config.yaml.example` in the same change.
