# Versa Proxmox Deployer

## Build

Static files are embedded into the binary via Go `//go:embed`. Any change to HTML, CSS, or JS requires rebuilding.

Always build for **all platforms** after any change, not just the local platform:

```bash
make release
```

This cross-compiles for darwin/amd64, darwin/arm64, linux/amd64, linux/arm64, and windows/amd64. Binaries go to `dist/`.

