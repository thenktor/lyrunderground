package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"lyrunderground/internal/library"
	"lyrunderground/internal/lrclib"
)

func TestTrackListAndDetailRenderTags(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	win := test.NewWindow(nil)
	win.Resize(fyne.NewSize(1200, 800))
	a := New(win)

	a.tracks = []*trackState{
		newTrackState(library.Track{
			FileName: "one.mp3", Artist: "Artist One", Title: "Title One",
			Album: "Album One", Duration: 200 * time.Second, Format: "MP3",
			Lyrics: "[00:01.00]Zeile",
		}),
		newTrackState(library.Track{
			FileName: "two.flac", Artist: "Artist Two", Title: "Title Two",
			Album: "Album Two", Duration: 210 * time.Second, Format: "FLAC",
		}),
	}
	a.selected = 0
	a.list.Refresh()
	a.updateDetail()
	win.Canvas().Content().Refresh()

	if !a.detailUpload.Disabled() {
		t.Fatal("upload button must be disabled before checking")
	}

	st := a.tracks[0]
	a.applyCheckOutcome(st, nil, lrclib.ErrNotFound)
	a.updateDetail()
	if a.detailUpload.Disabled() {
		t.Fatal("upload button must be enabled after a successful not-found check")
	}

	a.applyCheckOutcome(st, &lrclib.Record{
		ID:          42,
		PlainLyrics: func() *string { s := "Server-Lyrics"; return &s }(),
	}, nil)
	a.updateDetail()
	if !a.detailUpload.Disabled() {
		t.Fatal("upload button must be disabled when server lyrics exist")
	}

	markup := test.RenderObjectToMarkup(a.list)
	for _, want := range []string{
		"Artist One – Title One", "one.mp3",
		"Artist Two – Title Two", "two.flac",
	} {
		if !strings.Contains(markup, want) {
			t.Errorf("file list markup does not contain %q:\n%s", want, markup)
		}
	}
	for _, unwanted := range []string{"3:20", "3:30", "MP3", "FLAC"} {
		if strings.Contains(markup, unwanted) {
			t.Errorf("file list markup must not contain %q:\n%s", unwanted, markup)
		}
	}

	for _, want := range []string{
		"Datei:", "one.mp3", "Artist:", "Artist One", "Titel:", "Title One",
		"Album:", "Album One", "Dauer:", "Format:", "MP3", "Lyrics-Tag:",
		"vorhanden (LRC, 1 Zeile)",
	} {
		if !strings.Contains(a.detailTags.Text, want) {
			t.Errorf("detail tags do not contain %q:\n%s", want, a.detailTags.Text)
		}
	}
}

func TestApplyCheckOutcomeLoadsServerLyrics(t *testing.T) {
	a := &App{}
	st := newTrackState(library.Track{Artist: "Artist", Title: "Title", Duration: 100 * time.Second})
	synced := "[00:01.00]Server-Zeile"
	plain := "Server-Zeile"
	a.applyCheckOutcome(st, &lrclib.Record{
		ID:           42,
		PlainLyrics:  &plain,
		SyncedLyrics: &synced,
	}, nil)

	if st.lyrics != synced {
		t.Fatalf("lyrics = %q, want synced server lyrics", st.lyrics)
	}
	if !st.checked || !st.exists {
		t.Fatalf("state not marked as present: checked=%v exists=%v", st.checked, st.exists)
	}
	if st.importance != widget.SuccessImportance {
		t.Fatalf("importance = %v, want success", st.importance)
	}
}

func TestAddSingleFile(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	win := test.NewWindow(nil)
	win.Resize(fyne.NewSize(1200, 800))
	a := New(win)
	a.autoCheck = false

	a.addFile("../library/testdata/sample.opus")
	if len(a.tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(a.tracks))
	}
	if a.selected != 0 {
		t.Fatalf("selected = %d, want 0", a.selected)
	}
	if !strings.Contains(a.detailTags.Text, "sample.opus") {
		t.Fatalf("detail tags do not show file name: %q", a.detailTags.Text)
	}
	if !a.detailUpload.Disabled() {
		t.Fatal("upload must be disabled before checking even when tag lyrics exist")
	}

	// Adding the same file again must not create a duplicate entry.
	a.addFile("../library/testdata/sample.opus")
	if len(a.tracks) != 1 {
		t.Fatalf("duplicate file was added: got %d tracks", len(a.tracks))
	}
}

func TestHandleDropFiles(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	win := test.NewWindow(nil)
	win.Resize(fyne.NewSize(1200, 800))
	a := New(win)
	a.autoCheck = false

	path, err := filepath.Abs("../library/testdata/sample.opus")
	if err != nil {
		t.Fatal(err)
	}
	a.handleDrop(fyne.NewPos(0, 0), []fyne.URI{storage.NewFileURI(path)})
	if len(a.tracks) != 1 {
		t.Fatalf("got %d tracks after drop, want 1", len(a.tracks))
	}
	if !strings.Contains(a.detailTags.Text, "sample.opus") {
		t.Fatalf("detail tags do not show dropped file: %q", a.detailTags.Text)
	}

	a.handleDrop(fyne.NewPos(0, 0), []fyne.URI{storage.NewFileURI(path)})
	if len(a.tracks) != 1 {
		t.Fatalf("dropping the same file created a duplicate: got %d tracks", len(a.tracks))
	}
}

func TestClearList(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	win := test.NewWindow(nil)
	win.Resize(fyne.NewSize(1200, 800))
	a := New(win)
	a.autoCheck = false

	a.addFile("../library/testdata/sample.opus")
	if len(a.tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(a.tracks))
	}
	if a.clearBtn.Disabled() {
		t.Fatal("clear button must be enabled when the list has tracks")
	}

	a.clearList()
	if len(a.tracks) != 0 {
		t.Fatalf("got %d tracks after clearing, want 0", len(a.tracks))
	}
	if a.selected != -1 {
		t.Fatalf("selected = %d after clearing, want -1", a.selected)
	}
	if a.clearBtn.Disabled() == false {
		t.Fatal("clear button must be disabled on an empty list")
	}
	if a.detailTags.Text != "" {
		t.Fatalf("detail tags should be empty after clearing: %q", a.detailTags.Text)
	}
}

func TestPublishTrackInfoUsesServerMetadata(t *testing.T) {
	info := library.Track{
		Artist:   "Local Artist",
		Title:    "Local Title",
		Album:    "Local Album",
		Duration: 196 * time.Second,
	}
	record := &lrclib.Record{
		ArtistName: "Server Artist",
		TrackName:  "Server Title",
		AlbumName:  "Server Album",
	}
	got := publishTrackInfo(info, record)
	if got.ArtistName != "Server Artist" || got.TrackName != "Server Title" || got.AlbumName != "Server Album" {
		t.Fatalf("unexpected publish metadata: %+v", got)
	}

	got = publishTrackInfo(info, nil)
	if got.ArtistName != "Local Artist" || got.TrackName != "Local Title" || got.AlbumName != "Local Album" {
		t.Fatalf("unexpected fallback metadata: %+v", got)
	}
}

func TestLimitLyricsText(t *testing.T) {
	long := strings.Repeat("a", maxLyricsBytes+100)
	got, truncated := limitLyricsText(long)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(got) != maxLyricsBytes {
		t.Fatalf("len = %d, want %d", len(got), maxLyricsBytes)
	}

	boundary := strings.Repeat("a", maxLyricsBytes-1) + "ä"
	got, truncated = limitLyricsText(boundary)
	if !truncated {
		t.Fatal("expected truncation at UTF-8 boundary")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated text is not valid UTF-8: %q", got[len(got)-4:])
	}
	if len(got) > maxLyricsBytes {
		t.Fatalf("len = %d, want <= %d", len(got), maxLyricsBytes)
	}
}

func TestLyricsFieldLimit(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	win := test.NewWindow(nil)
	win.Resize(fyne.NewSize(1200, 800))
	a := New(win)
	a.autoCheck = false

	st := newTrackState(library.Track{
		Artist: "Artist", Title: "Title", Duration: 100 * time.Second,
	})
	a.tracks = []*trackState{st}
	a.selected = 0
	a.updateDetail()

	a.detailLyrics.SetText(strings.Repeat("x", maxLyricsBytes+100))
	if len(a.detailLyrics.Text) != maxLyricsBytes {
		t.Fatalf("field length = %d, want %d", len(a.detailLyrics.Text), maxLyricsBytes)
	}
	if len(st.lyrics) != maxLyricsBytes {
		t.Fatalf("state length = %d, want %d", len(st.lyrics), maxLyricsBytes)
	}
}
