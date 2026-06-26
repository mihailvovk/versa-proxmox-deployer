# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.3.1] - 2026-06-26

### Fixed

- **Crash on malformed `.md5`** - A truncated or HTML-error `.md5` companion file no longer panics the deploy goroutine; resolved checksums are validated as 32-char hex before use.
- **Cross-node placement** - Deploys now always land on the connected Proxmox node. A component targeting a different node is rejected up front instead of being silently created on the wrong host, and an unspecified node defaults to the connected host.
- **Data races** - Fixed unsynchronized access to the shared image list and the configured image-source slice; reconnecting during an in-flight deploy is now refused (409) so it can't close the SSH client the deploy is using.
- **Serial console robustness** - WebSocket writes are bounded by a deadline and serialized on a dedicated lock so a stalled browser tab can no longer wedge the idle-session reaper or leak sessions.
- **Remote shell safety** - SCP/`cat` remote paths are shell-escaped; SCP feeders surface stdin errors instead of panicking; the real remote exit code is reported.
- **S3 sources** - Region-less (us-east-1) bucket URLs list correctly, and nested key prefixes are preserved in download URLs.
- **Misc** - `FindISOByMD5` tolerates one unreadable ISO without losing dedup for the rest; download-task lookup no longer mistakes another task for ours; component detection matches `van` only as a whole token; legacy config migration is atomic; unknown `--components` values are rejected; `Connect()` no longer holds its lock across the network dial.

## [1.3.0] - 2026-06-26

### Added

- **Director TLS verification** - `status` verifies the Director certificate by default; pass `--insecure` for self-signed appliances (with a guided hint on failure).

### Changed

- **Per-user config location** - Config, TLS cert/key, logs, and the ISO cache now live under `os.UserConfigDir()` (`%AppData%` on Windows, `~/Library/Application Support` on macOS) instead of the working directory, keeping secrets out of cloud-synced folders. A legacy working-directory `config.json` is migrated automatically.
- **Atomic config writes** - `config.json` is written via a temp file + rename so a crash or concurrent writer can never leave a truncated config.
- **SSH authentication** - Connection now mirrors the `ssh` client: explicit key, then the user's default key, then password offered as both the `password` and `keyboard-interactive` methods, so it works against hosts that only accept one.

### Fixed

- **Secret exposure** - The web API no longer returns saved source passwords / SSH keys to the browser.
- **Hardening** - Same-origin check on the console WebSocket, HTML-escaping of interpolated names, fixed upload-key path handling, per-node resource validation, and surfacing of partial-discovery and network-apply failures.

## [1.2.0] - 2026-03-02

### Changed

- **Clearer image listings** - The images table and component ISO picker show the filename plus a simple source-type label (Dropbox / S3 / HTTP / SFTP / Local) instead of a truncated URL.

### Added

- **Per-VM deploy timestamp** - Each VM is tagged with `versa-ts-<unix>` at creation; the Deployed Instances view shows a relative "Deployed" column and sorts newest-first to make cleanup easier.

## [1.1.0] - 2026-02-25

### Added

- **S3 bucket source type** - Scan and download ISOs directly from public S3 buckets. Supports virtual-hosted and path-style URLs, plus `s3://` scheme. Uses ListObjectsV2 XML API with pagination.
- **Direct-to-Proxmox ISO download** - ISOs from HTTP/S3/Dropbox sources can now be downloaded directly on the Proxmox host, skipping the slow local-download-then-SCP path. Uses Proxmox native `download-url` API (pvesh) with wget/curl fallback. Falls back to SCP if Proxmox has no internet access.
- **ISO deduplication by MD5** - Before downloading, checks all Proxmox storages for an existing ISO with matching MD5 checksum. Reuses the existing file even if the filename differs.
- **Smart storage selection** - ISOs are uploaded to the storage with the most available space instead of an arbitrary first match.
- **Console diagnostics endpoint** - `/api/console/test?vmid=X` returns detailed JSON diagnostics for troubleshooting serial console issues.
- **Download progress logging** - Direct Proxmox downloads now show real-time progress from the Proxmox task log (wget speed, percentage, ETA).

### Changed

- **Serial console connection** - Now connects directly to the QEMU serial socket (`socat`) instead of `qm terminal`, which uses a termproxy wrapper on PVE 8.x that doesn't pipe output through SSH PTY.
- **SSL certificate skip** - All Proxmox direct downloads (pvesh, wget, curl) skip SSL verification to handle enterprise SSL decryption / self-signed certificates.

### Fixed

- **Windows path corruption** - Replaced `filepath.Dir`/`filepath.Join` with POSIX `path.Dir` and string concatenation for remote Linux paths. On Windows, `filepath` uses backslashes which corrupted Proxmox storage paths.
- **Source remove button** - Fixed URL comparison failing due to Go's JSON encoding of `&` as `\u0026`. Now uses index-based removal with URL verification and confirmation dialog.
- **S3 URL with index.html** - Strips trailing `index.html` from S3 URLs so the prefix is parsed correctly.
- **Source type re-detection** - Sources saved as "http" before the S3 type existed are now re-detected correctly on load.
- **pvesh download timeout** - pvesh `download-url` blocks until completion on PVE 8.x; now runs via `nohup` in background with task UPID polling to prevent SSH broken pipe.

## [1.0.2] - 2025-01-29

### Added

- **Serial Console** - Open a live serial terminal to any running VM directly from the web UI using xterm.js. Connects via SSH PTY (`qm terminal`) with full resize support and reconnection logic.
- **Per-instance interface reordering** - All network interfaces (including base interfaces like Management and Southbound) can now be reordered in the Instance Preview via up/down arrows. The ordering is persisted and applied when creating VMs.
- **Parallel loading on connect** - Discovery polling and deployment list fetching now run concurrently, reducing page load time after connecting.

### Changed

- **Removed CLI TUI** - Deleted the `ui/` package and all interactive terminal UI dependencies (`lipgloss`, `huh`, `progressbar`). The `add-source` command now requires the URL as a CLI argument instead of an interactive prompt. Status and release listing use plain `fmt` output.
- **Modular frontend** - Split monolithic `app.js` into focused modules: `connect.js`, `discovery.js`, `components.js`, `network.js`, `sources.js`, `deploy.js`, `deployments.js`, `console.js`, and `console.css`.
- **Slimmer binary** - Removing TUI dependencies reduces binary size and the dependency tree.

### Removed

- VNC console support (requires external noVNC library; serial console covers the primary use case).
- `ui/` package: `progress.go`, `prompts.go`, `tables.go` and their dependencies.

### Fixed

- Console tab switching no longer triggers stale reconnect attempts (added `_intentionalClose` flag and type guards on reconnect handlers).

## [1.0.0] - 2025-01-28

Initial release.

- Web UI at `http://localhost:1050` with HTTPS at `:1051`
- Auto-discovery of Proxmox nodes, storage pools, network bridges
- Multi-source ISO scanning (Dropbox, HTTP, SFTP, local)
- Standard and HA deployment modes
- Network topology SVG diagram
- Interface reordering for WAN and extra interfaces
- Deployment management (list, stop, delete VMs)
- Auto-rollback on failure
- VM tagging for identification
- Cross-platform binaries (macOS, Linux, Windows)
- CLI commands: `deploy`, `status`, `releases`, `add-source`, `generate-md5`
