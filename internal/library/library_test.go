package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhowden/tag"
)

func TestIsSyncedLyrics(t *testing.T) {
	tests := []struct {
		name   string
		lyrics string
		want   bool
	}{
		{"lrc", "[00:12.34]Eine Zeile\n[00:15.00]Noch eine", true},
		{"lrc colon", "[00:12:34]Eine Zeile", true},
		{"metadata only", "[ar:Künstler]\n[ti:Titel]\nText", false},
		{"plain", "Eine Zeile\nNoch eine", false},
		{"empty", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSyncedLyrics(test.lyrics); got != test.want {
				t.Fatalf("IsSyncedLyrics() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFilenameMetadata(t *testing.T) {
	artist, title := filenameMetadata("Künstler - Titel.mp3")
	if artist != "Künstler" || title != "Titel" {
		t.Fatalf("filenameMetadata() = %q, %q", artist, title)
	}

	artist, title = filenameMetadata("Nur ein Titel.flac")
	if artist != "" || title != "Nur ein Titel" {
		t.Fatalf("filenameMetadata() = %q, %q", artist, title)
	}
}

func TestMissingFields(t *testing.T) {
	track := Track{Artist: "A", Title: "T", Duration: 2 * time.Second}
	if missing := track.MissingFields(); len(missing) != 0 {
		t.Fatalf("MissingFields() = %v, want none", missing)
	}

	track = Track{Title: "T"}
	if missing := track.MissingFields(); strings.Join(missing, ",") != "Artist,Dauer" {
		t.Fatalf("MissingFields() = %v", missing)
	}
}

func TestScanFixtures(t *testing.T) {
	tracks, fileErrors, err := Scan(context.Background(), "testdata", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileErrors) != 0 {
		t.Fatalf("unexpected file errors: %v", fileErrors)
	}
	if len(tracks) != 5 {
		t.Fatalf("got %d tracks, want 5", len(tracks))
	}

	for _, track := range tracks {
		if track.Artist != "Testkünstler" {
			t.Errorf("%s: artist = %q", track.Path, track.Artist)
		}
		if track.Title != "Testtitel" {
			t.Errorf("%s: title = %q", track.Path, track.Title)
		}
		if track.Album != "Testalbum" {
			t.Errorf("%s: album = %q", track.Path, track.Album)
		}
		if track.Duration < 3*time.Second || track.Duration > 3500*time.Millisecond {
			t.Errorf("%s: duration = %s, want about 3.2s", track.Path, track.Duration)
		}
		if !strings.Contains(track.Lyrics, "Erste Zeile") {
			t.Errorf("%s: lyrics = %q", track.Path, track.Lyrics)
		}
	}
}

type fakeMetadata struct {
	tag.Metadata
	raw map[string]interface{}
}

func (f fakeMetadata) Lyrics() string              { return "" }
func (f fakeMetadata) Raw() map[string]interface{} { return f.raw }

func TestLyricsFromID3v22ULT(t *testing.T) {
	metadata := fakeMetadata{raw: map[string]interface{}{
		"ULT": &tag.Comm{Language: "eng", Text: "Zeile 1\nZeile 2"},
	}}
	if got := lyricsFromMetadata(metadata); got != "Zeile 1\nZeile 2" {
		t.Fatalf("lyricsFromMetadata() = %q", got)
	}
}

func TestReadTrack(t *testing.T) {
	track, err := ReadTrack("testdata/sample.opus")
	if err != nil {
		t.Fatal(err)
	}
	if track.Artist != "Testkünstler" || track.Title != "Testtitel" {
		t.Fatalf("unexpected metadata: %+v", track)
	}
	if track.Duration < 3*time.Second || track.Duration > 3500*time.Millisecond {
		t.Fatalf("unexpected duration: %s", track.Duration)
	}
	if !strings.Contains(track.Lyrics, "Erste Zeile") {
		t.Fatalf("unexpected lyrics: %q", track.Lyrics)
	}

	if _, err := ReadTrack("testdata/does-not-exist.mp3"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestScanSkipsHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	audio, err := os.ReadFile("testdata/sample.mp3")
	if err != nil {
		t.Fatal(err)
	}
	// macOS writes AppleDouble sidecars ("._track.mp3") next to files on FAT
	// and network volumes. They carry a supported extension but no audio.
	if err := os.WriteFile(filepath.Join(dir, "._sample.mp3"), []byte("Mac OS X        \x00\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden.flac"), []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.mp3"), audio, 0o644); err != nil {
		t.Fatal(err)
	}

	tracks, fileErrors, err := Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		var names []string
		for _, track := range tracks {
			names = append(names, track.FileName)
		}
		t.Fatalf("got %d tracks (%v), want only sample.mp3", len(tracks), names)
	}
	if len(fileErrors) != 0 {
		t.Fatalf("unexpected file errors: %v", fileErrors)
	}
	if tracks[0].FileName != "sample.mp3" {
		t.Fatalf("got track %q, want sample.mp3", tracks[0].FileName)
	}
	if tracks[0].Warnings != nil {
		t.Fatalf("unexpected warnings: %v", tracks[0].Warnings)
	}
}
