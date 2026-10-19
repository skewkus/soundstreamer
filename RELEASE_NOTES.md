# Sound Streamer release notes

Versions 1.0.1–1.0.5 are retrospective labels for the five preceding changes developed and tested in this project. Those builds originally used the unversioned executable name; separate historical versioned binaries were not published.

## 1.0.15 — Limit scans when adding library folders

- Scan only the newly added music/video folder and its selected subfolder scope, instead of rescanning every configured source.
- Preserve existing cached media, current playback and queue while adding a folder.
- Limit source-removal updates to the affected folder; retain entries still covered by overlapping configured folders.
- Keep category changes scan-free and the explicit Rescan all action available.

## 1.0.14 — Stereo, configurable audio quality and MIT licence

- Publish the application under the MIT licence, with embedded licence access and third-party notices.
- Include a spoken left/right `test.mp3` for checking channel separation.
- Default new and existing installations to stereo output at 48 kHz, 16-bit PCM.
- Add admin-only mono/stereo and 48/44.1/32/24 kHz controls, saved for application restart, with active/saved format status.
- Preserve interleaved left/right channels through file/video playback, PC audio, screen capture, packets and browser playback; mono remains available as a downmix.
- Keep 20 ms packets and the existing shared synchronization timeline at every supported format.
- Negotiate the actual format on each audio HTTP response; preserve sample rate/channel state throughout reconnects. Refresh old receiver pages after upgrading.

## 1.0.13 — Release privacy hygiene

- Build release EXEs with `-trimpath` to omit local source/build-folder paths.
- Exclude local environment files and additional private-key/keystore formats from source control.
- Preserve earlier binaries unchanged; their build-folder metadata is retained with their original bytes.

## 1.0.12 — Public release preparation

- Remove obsolete diagnostic executables, ZIPs, launchers, captured reports and development chat notes.
- Remove retired prototype pages, prototype-only metadata validation and pulse-generation code; retain `/listen` and `/watch` redirects to `/stream`.
- Preserve previous release executables in `bin/`; build scripts now write new versioned releases there.
- Retain current regression tests, build tooling and Windows tray validation.
- Update first-time setup, feature documentation, troubleshooting and contributor instructions for publication.

## 1.0.11 — Optional forced video transcoding

- Add admin-only Force transcoding with Original, 1080p, 720p and 480p presets, saved between restarts; default off.
- Keep automatic transcoding for unsupported files when Force transcoding is off.
- Preserve picture proportions, avoid upscaling and apply preset bitrate limits for weaker devices or connections.
- Apply to all rooms; playing library videos briefly re-buffer at the current position, and paused videos apply the settings on resume.
- Retain on-demand chunk encoding, shared audio synchronization and bounded caching; separate cached chunks by preset.
- Leave music and PC screen/audio capture unchanged.

## 1.0.10 — Stream page Control shortcut

- Add a touch-friendly Control button at the top of `/stream` to open `/control` in the same tab.
- Preserve the current hostname, HTTP/HTTPS protocol and port when switching pages.
- Match the selected theme and allow the header to wrap on small screens.

## 1.0.9 — HTTPS hostname for tray links

- Add an optional HTTPS hostname field under Admin Settings → Server, saved in `httpsHostname`.
- Use the configured hostname with the actual listening port in tray Admin, Control and Stream links, double-click, About and the logged browser URL.
- Validate hostname format and certificate-name matching. Apply after restart; existing configurations keep their local-address fallback.

## 1.0.8 — Admin-only PC capture

- Remove PC audio and screen capture buttons from `/control`.
- Reject capture-start requests through the public control API and dispatcher, including manually crafted requests.
- Retain capture controls exclusively in authenticated `/admin`; preserve normal public playback controls and stream viewing.

## 1.0.7 — Optional HTTPS

- Enable HTTPS and configure the certificate file, private key file and listening port under Admin Settings → Server.
- Validate PEM certificates/chains and matching unencrypted private keys before saving. Relative paths resolve beside the configuration file.
- Apply server changes on restart; HTTPS replaces HTTP on the configured port. HTTP remains the default for existing installations.
- Use TLS 1.2 or newer, HTTPS tray shortcuts and secure admin cookies on HTTPS connections.
- Show active and saved protocol/port in Settings and the tray Port dialog. Invalid TLS files at startup show an error; HTTP is not silently substituted.

## 1.0.6 — Windows tray application

- Launch directly to the Windows system tray without a command-prompt window.
- New generated Sound Streamer icon in the EXE, tray and browser tabs.
- Tray shortcuts for Admin, Control and Stream; Stop playback, Open log, About and Exit.
- Tray Port item shows the actual current port and explains the saved port for the next restart.
- Prevent duplicate instances using the same configuration directory.
- Configure the listening port in Admin Settings; default 8080, applied after restart. The existing `-addr` option remains an explicit override.
- Visible version footer, tray About version, Windows EXE version resources and versioned executable filenames.
- Clean exit preserves state, stops capture/decoder processes, shuts down HTTP and removes the tray icon. Startup failures show a dialog. Runtime diagnostics go to `soundstreamer.log`.
- Windows FFmpeg/FFprobe/PowerShell helpers launch without extra console windows.

## 1.0.5 — Optional subfolder indexing

- Include subfolders checkbox when adding music or video folders, off by default.
- Change the option on existing folders and rescan only the affected source.
- Inherit categories from recursive sources, with separately configured folders taking precedence.
- Deduplicate overlapping sources and preserve independently configured subfolders.
- Ignore NFO, TXT, images and other unsupported files.

## 1.0.4 — Required library categories

- Music default category; Concerts and Music Videos defaults for video.
- Required category when adding video folders and selectors for existing folders.
- Per-tab category filters combine with search and pagination.
- Add, rename and safely delete custom categories in Settings without rescanning.

## 1.0.3 — Filename title preference

- Default library titles to filenames without extensions.
- Toggle filename/embedded metadata titles in Settings without rescanning.
- Update library, queue and stream titles immediately while preserving cached metadata.

## 1.0.2 — Reuse the saved library at startup

- Restore a valid `soundstreamer-library.json` without automatically rescanning.
- Rebuild the library on startup only when its cache is missing or invalid.
- Preserve valid empty libraries and avoid unnecessary scans after replacing the EXE.

## 1.0.1 — Per-folder rescans

- Rescan beside every music/video source, plus Rescan all.
- Refresh only the selected source while preserving other cached metadata.
- Refresh browser library views even after a fast scan.
- Preserve cached entries if the requested folder cannot be read.

## Earlier unversioned baseline

Synchronized music and video, unified `/stream`, mobile `/admin` and `/control`, authentication, control freezing, queues/playlists, room naming and muting, themes, live FFmpeg transcoding, PC audio and 30/60 FPS screen capture, persistent lip-sync calibration, and fullscreen retention across media switches.
