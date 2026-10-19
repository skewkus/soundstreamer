# Sound Streamer

Sound Streamer broadcasts music, video and Windows PC audio/screen capture to browsers around your home. Run the server on one PC, open `/stream` on your TV, phone or other computer, and control the shared playback from a browser on your phone.

**Current version: 1.0.15.** The Windows tray executable is [bin/soundstreamer-1.0.15.exe](bin/soundstreamer-1.0.15.exe). Previous releases remain in `bin/`. See [release notes](RELEASE_NOTES.md) for changes.

The project is intended for a **trusted home network**. Browser/device support and synchronization depend on the receiving hardware, browser and connection; precise timing across every possible device is not guaranteed.

## What it does

- Synchronized browser playback of a shared music or video broadcast, including late joining and automatic timing corrections.
- One `/stream` page that switches between audio and video. No receiver app is required; each browser must join the stream once.
- Mobile-friendly playback controls, search, queues, queue reordering/removal, shuffle, repeat and saved playlists.
- Multiple music/video folders, required folder categories, optional subfolder scanning, individual folder rescans and a saved library cache.
- Browser room names, per-room mute, broadcast volume and per-browser timing calibration.
- Automatic on-the-fly transcoding for incompatible video, plus optional forced transcoding at Original, 1080p, 720p or 480p.
- Windows system-audio capture and primary-screen sharing with audio at 30 or 60 FPS, started by authenticated admins.
- A Windows system-tray application, configurable listening port, optional HTTPS and certificate-hostname tray links.
- Saved configuration, playback position and library state beside the executable, or in a specified data directory.

## Requirements

For the packaged Windows application:

- Windows 10 or newer, 64-bit, with an interactive desktop for the tray icon.
- **FFmpeg and FFprobe installed and available on the server's PATH.** They are separate dependencies and are not bundled with the EXE. The FFmpeg build must include the codecs your media requires; transcoding uses `libx264`.
- Windows PowerShell for PC audio/screen capture, plus an FFmpeg build with `gdigrab` for screen capture.
- A modern browser with JavaScript and Web Audio on each receiver. Transcoded video and live screen sharing additionally require Media Source support. Smart-TV browser support varies.
- A server that can encode video at playback speed when transcoding is needed, and a network that can carry a separate stream to each receiver.

Confirm the media tools in a new PowerShell window:

```powershell
ffmpeg -version
ffprobe -version
```

Restart Sound Streamer after changing PATH. Start with a current Chrome/Chromium browser when checking a new setup. Other browsers and TVs should be tested individually.

## Quick start

1. Download the current EXE from `bin/` and place it in a writable folder on the server PC, such as `C:\SoundStreamer`. You do not need the source tree or older binaries to run it.
2. Double-click `soundstreamer-1.0.15.exe`. It starts in the system tray without a command window. Check the tray overflow area if the icon is not immediately visible.
3. Double-click the tray icon or choose **Open Admin**. On first use, create an admin password of at least eight characters.
4. In **Admin → Settings**, add your music/video folders and select a category for each. Subfolders are excluded unless **Include subfolders** is checked.
5. Browse the library and select **Play** or **+ Queue**.
6. On another device, open `http://SERVER_LAN_IP:8080/stream`, replacing `SERVER_LAN_IP` with the server PC's home-network address. Press **Join stream** once.
7. Repeat on other receivers. Use **Name this room** on each stream page to identify it in the room controls.

On the server itself, the default address is `http://localhost:8080/admin`. Other devices must use a hostname or LAN address that reaches that PC; `localhost` on a phone refers to the phone itself. Allow the configured TCP port through Windows Firewall on your private/home network.

The tray menu provides **Open Admin**, **Open Control**, **Open Stream**, the current **Port**, **Stop playback**, **Open log file**, **About** and **Exit Sound Streamer**. Exit saves state and stops the server and capture processes. Closing a browser or the phone's control page does not stop the server.

## Browser pages and access

| Page | Purpose | Access |
| --- | --- | --- |
| `/stream` | Receive the current audio/video broadcast; join, name the room, calibrate timing and use fullscreen. The top Control button opens `/control` in the same tab. | No password |
| `/control` | Playback, library search, queues, playlists and room controls. No settings or PC-capture buttons. | No password; modifying actions can be frozen by admin |
| `/admin` | Playback plus settings, password changes, freeze/unfreeze and PC capture. | Password protected after initial setup |

`/listen` and `/watch` redirect to `/stream` for compatibility with old bookmarks.

**Freeze controls** blocks modifying actions through `/control`, including direct API calls, while playback continues. It does not prevent viewing `/stream`. Starting PC audio or screen capture requires an authenticated admin session, but once started, the resulting broadcast is available to stream viewers.

Do not expose this server directly to the public internet. HTTPS encrypts connections; it does not add authentication to the public stream or control pages. Complete admin setup before giving other users access to the server.

## Music, videos and the library

Use the **Music / Video** switch to keep the two libraries separate. Search combines with the selected category. Queue entries can be moved up/down or removed; the same track can appear more than once. Save the current selection and queue as a named playlist. Repeat supports off, current track and the queue.

New music folders default to the **Music** category. Video has **Concerts** and **Music Videos** defaults, with a required selection when adding a folder. Add, rename or delete categories in Settings; reassign folders before deleting a category they use. Existing video sources from older configurations may need their category selected.

**Include subfolders** is off by default and can be changed for each source. Separately configured subfolders can have their own categories and take precedence over a recursive parent. Overlapping sources do not create duplicate entries. Only recognized media extensions are indexed; NFO, TXT and image files are ignored.

Adding a new folder scans only that folder and its selected subfolder scope, preserving existing music and video caches. Category changes do not start a scan. Removing a source prunes its cached entries without rescanning other folders.

Use **Rescan** beside a folder after adding/removing files there, or **Rescan all** for the whole library. A valid `soundstreamer-library.json` is reused after restarts or EXE upgrades; a missing or invalid cache causes a startup scan. Keep that file when updating.

Library titles default to the filename without its extension. Choose **Embedded metadata** in Settings if preferred; switching does not require a rescan. Artist and album metadata remain available when present. Music ordering is artist first, then displayed title.

Common supported formats include MP3, FLAC, WAV, Ogg, Opus, M4A, AAC, WMA and AIFF audio, and MP4, MKV, AVI, MOV, WebM, WMV, M4V, MPEG, TS/M2TS and OGV video. Actual decoding depends on your FFmpeg build and the source file.

## Video transcoding and fullscreen

Compatible H.264/yuv420p MP4 video with AAC audio and a video start timestamp near zero plays directly. Incompatible video automatically uses short fragmented H.264 MP4 chunks encoded on demand. The original file is unchanged; the server does not convert the entire concert before playback. Silent video uses silent audio on the same shared timeline.

Under **Admin → Settings → Video playback**, enable **Force transcoding** to also convert compatible files for weaker receivers or slower connections. The switch defaults off; 720p is preselected and has no effect while it is off.

| Maximum resolution | Video encoder maximum-rate target |
| --- | --- |
| Original | 24 Mbit/s |
| 1080p | 8 Mbit/s |
| 720p | 4 Mbit/s |
| 480p | 2 Mbit/s |

These are video-encoder targets, not total network throughput guarantees. Presets preserve picture proportions and do not enlarge smaller videos. All rooms receive the same selected quality. **Apply video settings** saves the choice and briefly re-buffers a playing library video at its current position. Paused videos apply the settings when resumed. No app restart or rescan is required. Automatic transcoding for unsupported files remains enabled with Force transcoding off. Music and PC screen capture are unaffected.

The encoder cache is bounded to 16 completed chunks and 64 MiB, with at most two encoders running concurrently. Video remains 8-bit SDR H.264; HDR tone mapping, subtitles, alternate audio-track selection and hardware-encoding controls are not currently provided.

Use **Fullscreen** on the stream page. Its button hides after inactivity and returns with pointer movement or keyboard focus. Fullscreen stays active when switching to music, so a later video can use the same fullscreen player. Switching to the Control page navigates away and ends playback in that browser tab.

## Share the server PC's audio or screen

These features are Windows-only and available in authenticated `/admin`.

- **Stream PC audio** captures the server's default sound output using WASAPI loopback, Windows PowerShell and FFmpeg. Stereo Mix or a virtual audio driver is not required.
- **Stream PC screen + audio** captures the primary monitor and default sound output. Choose 30 or 60 FPS under **Settings → PC screen streaming** before starting. Output is scaled to 1280 × 720.

Receivers view/listen through the same `/stream` page and cannot control the captured PC. Stop capture, use Stop, or select a library item to return to file playback. Seeking is unavailable for live capture. Capture does not automatically restart after an app restart, even with resume enabled. If you change the default Windows sound device, stop and start capture again.

Keep the server's own listening page muted or disconnected when capturing its audio, to avoid feeding delayed playback back into the capture. Keep the receiving stream page off the monitor being shared to avoid a repeated-screen effect.

For screen-share lip sync, use **Screen stream audio offset (milliseconds)**. Negative values make audio earlier relative to the picture; positive values delay it. The range is −3000 to +3000 ms. **Apply offset** saves and applies the value while streaming; rooms briefly reconnect to clear queued audio. Larger negative adjustments can add shared buffering. This setting affects screen sharing, not music or audio-only capture.

## Timing, volume and limitations

The default **1500 ms shared buffer** gives packets time to reach receivers before their scheduled playback time. Devices aim to follow the same server timeline rather than playing immediately on arrival. The delay is behind the source, not an intended delay between rooms.

A smaller buffer improves responsiveness but leaves less tolerance for temporary network delays; a larger one increases tolerance and latency. The command-line `-buffer` option accepts 500 ms to 5 s. Keep 1500 ms initially. Per-room timing calibration on `/stream` shifts that browser's picture and audio together; screen-share audio offset separately adjusts lip sync.

Other current limits:

- Audio defaults to **48 kHz, 16-bit stereo**. Mono and lower sample rates are available under Audio quality; changes apply after restarting the server. Mono sources do not gain stereo separation when output as stereo.
- Browser autoplay rules require joining the stream. Sleeping devices, background tabs, Bluetooth audio and TV processing can introduce delays or interrupt playback.
- Initial joining, seeking, track changes and resynchronization can briefly buffer. Queue transitions are not guaranteed gapless.
- High-resolution files may start slowly because of network throughput, client decoding or server transcoding. A lower forced-transcode preset can help.
- Browser timing diagnostics are estimates, not measurements of sound leaving speakers or pixels appearing on displays.

The broadcast volume and per-room mute are available from the controls; normal device volume remains useful. Clearing browser cookies removes its stored room identity/name. Different tabs in one browser share that room identity.

## Audio quality

Under **Admin → Settings → Audio quality**, select **Stereo** (default) or **Mono**, and a sample rate of **48 kHz** (default), **44.1 kHz**, **32 kHz** or **24 kHz**. Save, then exit and restart Sound Streamer. The page shows the active format and what is saved for the next restart. The same format is used for music, video audio, silent-video timing, PC audio and screen capture in every room. Existing installations also default to stereo/48 kHz when first upgraded; an explicitly saved mono setting is retained thereafter.

Audio remains signed 16-bit PCM. Stereo preserves distinct left/right channels from stereo sources; mono downmixes them. Approximate uncompressed audio bandwidth per receiver, excluding headers:

| Sample rate | Stereo | Mono |
| --- | --- | --- |
| 48 kHz | 1.536 Mbit/s | 0.768 Mbit/s |
| 44.1 kHz | 1.411 Mbit/s | 0.706 Mbit/s |
| 32 kHz | 1.024 Mbit/s | 0.512 Mbit/s |
| 24 kHz | 0.768 Mbit/s | 0.384 Mbit/s |

Lower rates reduce bandwidth and high-frequency detail. They do not change the shared buffer or guarantee lower latency; keep 48 kHz unless bandwidth is a problem. Receiver browsers resample for their audio hardware as necessary. After upgrading from a mono-only release, refresh receiver pages to load the updated player before joining. Stream packets remain 20 ms at all offered formats, and both stereo channels share a timestamp.

### Stereo test file

The included [test.mp3](test.mp3) has spoken left-only and right-only checks, separate channel tones, alternating tones, and a final both-speaker section. Add its folder as a music source (or copy it into an existing source and rescan that folder), then play it. Use Stereo output and verify that each announcement is heard only from its named speaker. The later stereo section should play from both speakers. In Mono mode, the left/right checks are intentionally mixed together.

## Port and HTTPS

Change **Admin → Settings → Server → Listening port**, save, then exit and restart the app. The default is 8080. The tray Port item shows the active port and any saved protocol/port change. Update receiver bookmarks and firewall rules when changing it.

To enable HTTPS:

1. Set the port and enable **HTTPS** in Server settings.
2. Enter the server-side paths to a PEM certificate/full-chain file and its matching **unencrypted** PEM private key. RSA, ECDSA and Ed25519 keys are supported. Put the server certificate first in a chain.
3. Optionally set **HTTPS hostname for tray links**, such as `streamer.example.com`, without a scheme, port or path. It must be covered by the certificate and resolve to the server PC; the setting does not create a DNS record.
4. Save, exit and restart. Open `https://streamer.example.com:PORT/admin` or `/stream` using a name covered by the certificate and a certificate chain trusted by each device.

HTTPS replaces HTTP on the configured port. Absolute certificate paths are recommended; relative paths resolve beside `soundstreamer.json`. Only paths are saved, not the certificate/key contents. Files and hostname matching are checked before saving and again at startup. Restart after renewing certificate files.

Tray shortcuts use the configured hostname and actual listening port. If the hostname is blank, the local-address fallback may not match your certificate. Invalid files prevent HTTPS startup; the app does not silently fall back to HTTP. To recover, stop it and correct `certFile`, `keyFile` or `httpsHostname`, or set `httpsEnabled` to `false` in `soundstreamer.json`.

## Saved files, updates and command-line options

The app normally stores files **beside the executable**, independently of its working directory:

| File | Purpose |
| --- | --- |
| `soundstreamer.json` | Settings, password hash, login-signing secret, folders/categories, selection, position, queue, playlists and room settings |
| `soundstreamer-library.json` | Cached library metadata, reused without rescanning |
| `soundstreamer.log` | Runtime diagnostics; use the tray's Open log file item |
| `soundstreamer.log.1` | Previous log when the startup log exceeds 5 MiB |

Keep configuration private. Passwords use salted PBKDF2-HMAC-SHA256 hashes. Change the password in Admin Settings to update hashing and invalidate existing admin sessions. For password recovery, stop the app, set `adminPasswordHash` to an empty string, restart and create a new password in `/admin`.

To upgrade, exit the running app, back up both JSON files, and put the new EXE in the **same data folder**. Launch the new EXE and refresh browser pages. Keep the JSON files so a new version can reuse the library and settings. An EXE moved into another folder uses another data folder unless `-data-dir` is supplied.

Examples from a source checkout, whose binaries are in `bin/`:

```powershell
# Use a separate, stable data folder for any release version.
.\bin\soundstreamer-1.0.15.exe -data-dir "C:\SoundStreamerData"

# Override the saved port and choose the shared buffer.
.\bin\soundstreamer-1.0.15.exe -addr :8081 -buffer 1500ms

# Start and repeat a file immediately.
.\bin\soundstreamer-1.0.15.exe -file "C:\Music\song.mp3"
.\bin\soundstreamer-1.0.15.exe -video "C:\Videos\concert.mkv"
```

`-file` and `-video` are mutually exclusive. `-addr` overrides the saved listening address/port for that run; HTTPS still follows the saved HTTPS settings. Only one instance may use a configuration directory at a time. Resume-on-startup is optional; the default restores the current selection paused.

## Troubleshooting

| Problem | Check |
| --- | --- |
| Other devices cannot connect | Server hostname/IP, private-network firewall rule, configured port, matching HTTP/HTTPS scheme and that the server is running. |
| Missing media or playback errors | `ffmpeg` and `ffprobe` on PATH; media file accessibility; the log; then rescan the affected source. |
| HTTPS warning or startup failure | Certificate hostname, trust chain, validity, matching key and file paths. Use the recovery instructions above if the app cannot start. |
| A room drops out or drifts | Browser/device sleep settings, Wi-Fi stability, hardware decoding, output latency and room calibration. Test with built-in speakers before Bluetooth. |
| Screen-sharing lips and sound differ | Adjust the screen-stream audio offset, not the room calibration. |
| No tray icon | Tray overflow area, duplicate-instance/startup error dialog and folder write permissions. |
| Defender quarantines an EXE | A previous unsigned build triggered a behaviour detection and was submitted for Microsoft review; no review verdict is recorded here. Check Protection History and submit suspected false positives to Microsoft. An antivirus exception is not a required installation step or proof of safety. |

## Build and contribute

See [CONTRIBUTING.md](CONTRIBUTING.md) for builds, tests, repository layout and issue reports. The source requires Go 1.22 or newer. Media tests generate temporary fixtures; no personal media or certificate files are required.

## Licence

Sound Streamer is free and open-source software under the [MIT License](LICENSE). You may download, use, modify and redistribute it, including commercially, provided the copyright and permission notice is retained. It is supplied without warranty. See [third-party notices](THIRD_PARTY_NOTICES.md) for the Go runtime and external tools; FFmpeg/FFprobe are not bundled and retain their own licences. The application also serves its embedded licence at `/license`.
