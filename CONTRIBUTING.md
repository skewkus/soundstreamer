# Developing Sound Streamer

## Repository layout

| Location | Contents |
| --- | --- |
| Root `*.go` | Server, configuration, media library, capture, synchronization and platform-specific tray code |
| Root `*_test.go` | Regression tests for current features, including access controls, timing, HTTPS and real video encoding |
| `web/` | Embedded browser player, control/admin interface, scripts and themes |
| `capture/loopback.ps1` | Embedded Windows WASAPI loopback capture implementation; required at runtime through the EXE |
| `branding/` | App icon assets and generation provenance |
| `winres/` | Windows resource template |
| `rsrc_windows_amd64.syso` | Generated Windows icon/version/manifest resources, linked into Windows builds |
| `scripts/` | Current build scripts/resource generator and native Windows tray smoke test |
| `bin/` | Current and historical versioned Windows release executables |

Do not add personal configuration, library caches, logs, private keys, certificates, diagnostic captures or media files to the public repository. `.gitignore` protects common local files, but it does not remove anything already committed; review staged files and history before publishing.

## Prerequisites

- Go 1.22 or newer.
- FFmpeg and FFprobe on PATH for playback and media integration tests.
- Windows for the native tray test and actual PC capture. Core application/media tests can run on Linux.
- Internet access on the first Windows resource build to download the pinned build-only `github.com/tc-hib/go-winres@v0.3.3` compiler and its dependencies.

There are no external Go runtime-module dependencies and no frontend package installation or build step. HTML, JavaScript, CSS, the PNG icon and loopback script are embedded in the executable.

## Build Windows releases

From PowerShell in the project root:

```powershell
.\scripts\build-windows.ps1
```

Or from Bash on a Go development machine:

```sh
bash scripts/build-windows.sh
```

Both scripts read `version.go`, regenerate icon/version/manifest resources and write `bin/soundstreamer-VERSION.exe`. They use `-trimpath` to omit local build-folder paths, and build Windows amd64 with the GUI subsystem, so launching the release does not create a console window. Earlier versioned binaries are retained unchanged; releases before 1.0.13 may contain the local source-folder path in Go debug metadata. This is build metadata, not a stored application password or key.

An optional output directory can be supplied for an isolated build:

```powershell
.\scripts\build-windows.ps1 -OutputDirectory "C:\Temp\SoundStreamerBuild"
```

```sh
bash scripts/build-windows.sh /tmp/soundstreamer-build
```

Use these scripts when changing the version or icon. The zero version values in `winres/winres.json` are template placeholders; the resource generator replaces them using `version.go`. The generated `.syso` must be available for a plain Windows `go build` to have the correct resources. The standalone `scripts/build-resources.go` program is build tooling, not another server.

For core development with disposable data:

```sh
go run -buildvcs=false . -data-dir /tmp/soundstreamer-dev
```

On Windows, use a writable temporary Windows data path instead. Never point development runs at an active production data folder. The `-buildvcs=false` option also supports source archives without Git metadata.

## Validation

Run from the project root:

```sh
go test -buildvcs=false -race ./...
go vet -buildvcs=false ./...
```

The race detector requires a supported host toolchain with a C compiler. Without one, run `go test -buildvcs=false ./...`; run race checks on a suitable development machine before release. Media integration tests skip when FFmpeg/FFprobe are unavailable. They otherwise generate temporary media, including incompatible codecs and forced-transcoding profiles.

Tests cover configurable PCM formats and stereo channel separation, shared packets/late joining, slow receivers, direct video ranges, on-demand encoding, shared bounded caches, codec compatibility, quality changes, queues/playlists, library scans/categories/recursion, title preferences, startup persistence, authentication, freeze, admin-only capture, HTTPS, room mute and screen lip-sync settings. `*_test.go` files are compiled by `go test`, not included in release executables.

On an interactive Windows desktop, after building:

```powershell
.\scripts\test-windows-tray.ps1
```

This starts the current `bin/` EXE with unique temporary data and a loopback-only ephemeral port, exercises the API, changes/saves a port and closes the tray window gracefully. It removes its own temporary data and does not change Defender settings. To check another release, use `-Executable "C:\Path\soundstreamer-VERSION.exe"`.

HTTPS smoke testing accepts disposable PEM certificate/key files and a hostname covered by that test certificate:

```powershell
.\scripts\test-windows-tray.ps1 -CertFile "C:\Temp\test.crt" -KeyFile "C:\Temp\test.key" -HTTPSHostname localhost
```

The test pins the supplied certificate only inside its own test process; it does not install a trusted certificate or alter application TLS verification.

Automated checks cannot measure speaker output or TV display latency. Before a public release, check real devices side by side for joining, sustained playback, seek/pause/resume, queue transitions, direct/transcoded video, forced presets and PC capture. Record the devices, browsers, connection types and any room offsets used.

## Issues and changes

When reporting a problem, include the app version, Windows/browser versions, source type, direct or transcoded playback, relevant settings and whether the problem affects one room or all rooms. For capture, distinguish room-to-room timing from picture/audio lip sync. Redact private paths and personal information from logs; do not upload `soundstreamer.json`, media or private keys.

Keep changes focused and run checks appropriate to them. Update the README/release notes for user-visible changes. For a new release, update `version.go`, build, verify Windows version metadata and the tray workflow, and retain earlier EXEs in `bin/`. Contributions must be compatible with the project’s MIT licence. Retain attribution and third-party notices when contributing external code or assets.
