# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
