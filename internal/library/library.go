// Package library scans a local music folder and extracts the metadata needed
// for uploading lyrics to LRCLIB.
package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dhowden/tag"
)

// Track contains the local metadata of one audio file.
type Track struct {
	Path     string
	FileName string
	Format   string

	Artist   string
	Title    string
	Album    string
	Duration time.Duration
	Lyrics   string

	// Warnings contains non-fatal problems, for example an unreadable tag or a
	// duration that could not be determined.
	Warnings []string
}

// DurationSeconds returns the duration rounded to whole seconds, which is the
// representation used by the LRCLIB API.
func (t Track) DurationSeconds() int {
	return int(math.Round(t.Duration.Seconds()))
}

// MissingFields returns the metadata fields that are required for an upload but
// are not available.
func (t Track) MissingFields() []string {
	var missing []string
	if strings.TrimSpace(t.Artist) == "" {
		missing = append(missing, "Artist")
	}
	if strings.TrimSpace(t.Title) == "" {
		missing = append(missing, "Titel")
	}
	if t.DurationSeconds() < 1 {
		missing = append(missing, "Dauer")
	}
	return missing
}

// HasRequiredMetadata reports whether artist, title and duration are present.
func (t Track) HasRequiredMetadata() bool {
	return len(t.MissingFields()) == 0
}

// DisplayName returns a human readable track name.
func (t Track) DisplayName() string {
	switch {
	case t.Artist != "" && t.Title != "":
		return t.Artist + " – " + t.Title
	case t.Title != "":
		return t.Title
	case t.Artist != "":
		return t.Artist
	default:
		return t.FileName
	}
}

var lrcTimestamp = regexp.MustCompile(`(?m)^[ \t]*\[\d{1,3}:\d{2}(?:[.,:]\d{1,3})?\]`)

// IsSyncedLyrics reports whether s looks like LRC-formatted lyrics with line
// timestamps. Plain text lyrics without timestamps return false.
func IsSyncedLyrics(s string) bool {
	return lrcTimestamp.MatchString(s)
}

// ScanProgress is called while files are being parsed. done and total are
// zero-based; the final call has done == total and an empty path.
type ScanProgress func(done, total int, path string)

var supportedExtensions = map[string]struct{}{
	".mp3":  {},
	".ogg":  {},
	".opus": {},
	".flac": {},
	".m4a":  {},
}

// Scan recursively reads all supported audio files below root. Individual file
// errors are returned in errs, while successfully parsed tracks are still
// returned. The returned error is only set for fatal problems such as an
// unreadable root directory or a cancelled context.
func Scan(ctx context.Context, root string, progress ScanProgress) (tracks []Track, errs []error, err error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil, errors.New("kein Ordner angegeben")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, fmt.Errorf("Ordner kann nicht gelesen werden: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s ist kein Ordner", root)
	}

	var files []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, walkErr))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		// Skip hidden files and macOS AppleDouble sidecars ("._track.mp3",
		// "._sample.flac"). They carry a supported extension but contain no
		// readable audio, so scanning them only produces junk rows.
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if _, ok := supportedExtensions[strings.ToLower(filepath.Ext(path))]; ok {
			files = append(files, path)
		}
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return nil, errs, walkErr
		}
		return nil, errs, fmt.Errorf("Ordner kann nicht durchsucht werden: %w", walkErr)
	}

	sort.Strings(files)
	tracks = make([]Track, 0, len(files))
	for i, path := range files {
		if err := ctx.Err(); err != nil {
			return tracks, errs, err
		}
		if progress != nil {
			progress(i, len(files), path)
		}
		track, err := readTrack(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		tracks = append(tracks, track)
	}
	if progress != nil {
		progress(len(files), len(files), "")
	}
	return tracks, errs, nil
}

// IsSupportedFile reports whether path has a supported audio file extension.
func IsSupportedFile(path string) bool {
	_, ok := supportedExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

// ReadTrack reads a single supported audio file and returns its metadata.
func ReadTrack(path string) (Track, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Track{}, errors.New("kein Dateipfad angegeben")
	}
	if !IsSupportedFile(path) {
		return Track{}, fmt.Errorf("nicht unterstütztes Dateiformat %q", filepath.Ext(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		return Track{}, err
	}
	if info.IsDir() {
		return Track{}, fmt.Errorf("%s ist ein Ordner", path)
	}
	return readTrack(path)
}

func readTrack(path string) (Track, error) {
	ext := strings.ToLower(filepath.Ext(path))
	t := Track{
		Path:     path,
		FileName: filepath.Base(path),
		Format:   strings.TrimPrefix(strings.ToUpper(ext), "."),
	}

	f, err := os.Open(path)
	if err != nil {
		return t, err
	}
	defer f.Close()

	if metadata, tagErr := tag.ReadFrom(f); tagErr != nil {
		if !errors.Is(tagErr, tag.ErrNoTagsFound) {
			t.Warnings = append(t.Warnings, "Tags konnten nicht gelesen werden: "+tagErr.Error())
		}
	} else {
		t.Artist = cleanText(metadata.Artist())
		t.Title = cleanText(metadata.Title())
		t.Album = cleanText(metadata.Album())
		t.Lyrics = lyricsFromMetadata(metadata)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Warnings = append(t.Warnings, "Datei konnte nicht zurückgespult werden: "+err.Error())
	} else if duration, err := durationFor(f, ext); err != nil {
		t.Warnings = append(t.Warnings, "Dauer konnte nicht ermittelt werden: "+err.Error())
	} else if duration > 0 {
		t.Duration = duration
	} else {
		t.Warnings = append(t.Warnings, "Dauer konnte nicht ermittelt werden")
	}

	fallbackArtist, fallbackTitle := filenameMetadata(filepath.Base(path))
	if t.Artist == "" {
		t.Artist = fallbackArtist
	}
	if t.Title == "" {
		t.Title = fallbackTitle
	}
	return t, nil
}

// lyricsFromMetadata reads the standardized lyrics field and, as a fallback,
// common non-standard TXXX/comment fields written by some taggers.
func lyricsFromMetadata(metadata tag.Metadata) string {
	if lyrics := normalizeLyrics(metadata.Lyrics()); lyrics != "" {
		return lyrics
	}
	for key, value := range metadata.Raw() {
		name := strings.ToLower(strings.ReplaceAll(key, " ", ""))
		if name != "lyrics" && name != "unsyncedlyrics" {
			continue
		}
		if text, ok := value.(string); ok {
			if lyrics := normalizeLyrics(text); lyrics != "" {
				return lyrics
			}
		}
	}
	// ID3v2.2 uses ULT instead of USLT. dhowden/tag exposes it in Raw()
	// but its Lyrics() helper does not read the ID3v2.2 frame name.
	for _, key := range []string{"ULT", "USLT"} {
		if comm, ok := metadata.Raw()[key].(*tag.Comm); ok {
			if lyrics := normalizeLyrics(comm.Text); lyrics != "" {
				return lyrics
			}
		}
	}
	if comm, ok := metadata.Raw()["TXXX"].(*tag.Comm); ok {
		description := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(comm.Description), " ", ""))
		if description == "lyrics" || description == "uslt" || description == "unsyncedlyrics" {
			return normalizeLyrics(comm.Text)
		}
	}
	return ""
}

func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimSpace(s)
}

func normalizeLyrics(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func filenameMetadata(name string) (artist, title string) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if parts := strings.SplitN(stem, " - ", 2); len(parts) == 2 {
		artist = cleanText(parts[0])
		title = cleanText(parts[1])
		return artist, title
	}
	return "", cleanText(stem)
}
