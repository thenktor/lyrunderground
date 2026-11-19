# lyrunderground

Lyrunderground (display name: **Lyrics From The Underground**) is a small desktop
application built with Go and Fyne. It reads a local album folder or individual
audio files and uploads lyrics to [LRCLIB](https://lrclib.net).

## Features

- Read an album folder or individual audio files with `.mp3`, `.ogg`, `.opus`,
  `.flac`, and `.m4a`.
- Read artist, title, album, and existing lyrics from file tags. The supported
  lyrics tags are listed below.
- Determine track duration directly from the audio data:
  - MP3 via MPEG frames,
  - Ogg Vorbis via the granule position,
  - Ogg Opus via granule position and pre-skip,
  - FLAC via `StreamInfo`,
  - M4A via `mvhd`/`mdhd`.
- For the selected file, the detail pane shows artist, title, album, duration,
  format, filename, and the status of the embedded lyrics tag.
- A multiline lyrics field for the selected file. Existing tag lyrics are
  filled in automatically.
- Per selected file:
  - **Check** queries LRCLIB for existing lyrics. Existing server lyrics are
    loaded directly into the lyrics field.
  - **Upload** becomes available only after a check has confirmed that LRCLIB
    does not yet have lyrics for the track.
  - **Instrumental (no lyrics)** intentionally sends no lyrics fields; LRCLIB
    then marks the track as instrumental.
- **Upload all** processes the list sequentially. Requests are throttled and
  the `Retry-After` header is honored for HTTP 429/503 responses.
- LRC lyrics with timestamps (`[00:12.34]…`) are sent as `syncedLyrics`; plain
  text is sent as `plainLyrics`.
- The proof of work required for `POST /api/publish` is solved automatically.

## Supported lyrics tags

Scanning only reads existing lyrics. Tags are never written back to the files.

| Format | Lyrics tags read |
|---|---|
| **MP3 / ID3v2.2** | `ULT` |
| **MP3 / ID3v2.3 + v2.4** | `USLT` |
| **MP3 / non-standard** | `TXXX` with description `LYRICS`, `USLT`, or `UNSYNCEDLYRICS` |
| **FLAC** | Vorbis comment `LYRICS`, `UNSYNCEDLYRICS` |
| **Ogg Vorbis** | Vorbis comment `LYRICS`, `UNSYNCEDLYRICS` |
| **Ogg Opus** | Vorbis comment `LYRICS`, `UNSYNCEDLYRICS` |
| **M4A / MP4** | `©lyr` |

Currently not read include:

- ID3v2 `SYLT` (synchronized lyrics)
- Vorbis comment `SYNCEDLYRICS`
- WMA/ASF `WM/Lyrics` (WMA is not supported as a file type)

Tag detection is implemented in `internal/library/library.go` in
`lyricsFromMetadata()`. If the lyrics contain LRC timestamps such as
`[00:12.34]`, they are uploaded as `syncedLyrics`; otherwise they are uploaded
as `plainLyrics`.

## Running

Requirements: Go 1.27.1 or newer and the Fyne system dependencies (for example,
Xcode Command Line Tools on macOS).

```bash
go run .
```

Or build a standalone binary for your operating system:

```bash
make darwin
```

The version can also be printed directly:

```bash
go run . -version
# or
make darwin
bin/darwin/lyrunderground -version
```

When building with the `Makefile`, the Git tag and commit SHA are embedded in
the binary via `codeberg.org/unixery/buildinfo`.

## Usage

1. Click **Open album folder**, or enter the path and press Enter. The folder is
   read immediately. Alternatively, use **Open file** to add individual audio
   files, or drag files and folders onto the window.
2. Click a file in the list on the left.
3. In the detail pane, review tags and existing lyrics or enter new lyrics.
   The LRCLIB check starts automatically after opening; **Check** can be used at
   any time to run it again manually.
4. **Upload** becomes available only once LRCLIB has no lyrics for the selected
   file. Alternatively, use **Upload all** for the entire list.
5. **Clear list** removes all entries; the folder path remains available for
   another scan.

Missing required fields (artist, title, or duration) are shown per track; check
and upload are disabled for those files. The album name is sent when it is
present in the tags.

## LRCLIB notes

- The application identifies itself with its own `User-Agent`. The name is
  defined in `internal/appinfo/appinfo.go` and used in
  `internal/lrclib/client.go`; adjust it for custom builds.
- Requests are sent sequentially with at least 350 ms between them.
- On `429 Too Many Requests` or `503 Service Unavailable`, the `Retry-After`
  header is honored and the request is retried if necessary.
- During upload, a challenge is requested first, and a publish token is
  computed from it. This can take a few seconds depending on server load.
- Existing server lyrics are not overwritten; the upload button is disabled
  accordingly after a successful check.
- LRCLIB does not document a fixed propagation delay. After a successful upload,
  it can take up to 24 hours before the lyrics become available.

## Tests

```bash
go test ./...
```

The test fixtures under `internal/library/testdata` are short, self-generated
silent audio files with metadata for all five supported formats.

## Project structure

```text
main.go                      entry point / Fyne window
internal/appinfo/            application names
internal/library/            file/folder scanning, tag and duration detection
internal/lrclib/             LRCLIB API client including proof of work
internal/ui/                 Fyne user interface
```
