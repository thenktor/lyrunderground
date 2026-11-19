package lrclib

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lyrunderground/internal/appinfo"
)

func testClient(server *httptest.Server) *Client {
	return &Client{
		BaseURL:            server.URL,
		HTTPClient:         server.Client(),
		UserAgent:          appinfo.NAME + "-test",
		MinRequestInterval: 0,
		MaxRetries:         0,
	}
}

func TestCheckFound(t *testing.T) {
	var gotQuery, gotAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": 42,
			"trackName": "Titel",
			"artistName": "Künstler",
			"albumName": "Album",
			"duration": 123,
			"instrumental": false,
			"plainLyrics": "Zeile 1\nZeile 2",
			"syncedLyrics": null,
			"lyricsfile": ""
		}`)
	}))
	defer server.Close()

	client := testClient(server)
	record, err := client.Check(context.Background(), TrackInfo{
		ArtistName: "Künstler",
		TrackName:  "Titel",
		AlbumName:  "Album",
		Duration:   123456 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != 42 || !record.HasLyrics() {
		t.Fatalf("unexpected record: %+v", record)
	}
	if gotAgent != appinfo.NAME+"-test" {
		t.Fatalf("User-Agent = %q", gotAgent)
	}
	for _, part := range []string{"artist_name=", "track_name=", "album_name=", "duration=123"} {
		if !strings.Contains(gotQuery, part) {
			t.Errorf("query %q does not contain %q", gotQuery, part)
		}
	}
}

func TestCheckNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Failed to find specified track","name":"TrackNotFound","statusCode":404}`)
	}))
	defer server.Close()

	_, err := testClient(server).Check(context.Background(), TrackInfo{ArtistName: "A", TrackName: "T"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestPublish(t *testing.T) {
	var gotToken string
	var gotPayload publishPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request-challenge":
			if r.Method != http.MethodPost {
				t.Errorf("challenge method = %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"prefix":"abc123","target":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`)
		case "/api/publish":
			gotToken = r.Header.Get("X-Publish-Token")
			if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
				t.Errorf("decode payload: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := testClient(server).Publish(context.Background(), PublishRequest{
		TrackInfo: TrackInfo{
			ArtistName: "Künstler",
			TrackName:  "Titel",
			AlbumName:  "Album",
			Duration:   95 * time.Second,
		},
		Lyrics: "[00:01.00]Zeile",
		Synced: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "abc123:0" {
		t.Fatalf("publish token = %q", gotToken)
	}
	if gotPayload.TrackName != "Titel" || gotPayload.ArtistName != "Künstler" || gotPayload.Duration != 95 {
		t.Fatalf("unexpected payload: %+v", gotPayload)
	}
	if gotPayload.SyncedLyrics == nil || *gotPayload.SyncedLyrics != "[00:01.00]Zeile" {
		t.Fatalf("unexpected synced lyrics: %+v", gotPayload.SyncedLyrics)
	}
	if gotPayload.PlainLyrics != nil {
		t.Fatalf("unexpected plain lyrics: %+v", gotPayload.PlainLyrics)
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"duration: must be between 1 and 3600","name":"ValidationError","statusCode":400}`)
	}))
	defer server.Close()

	_, err := testClient(server).Check(context.Background(), TrackInfo{ArtistName: "A", TrackName: "T"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.StatusCode != 400 || apiErr.Name != "ValidationError" {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
}

func TestPublishInstrumental(t *testing.T) {
	var gotPayload publishPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request-challenge":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"prefix":"abc123","target":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`)
		case "/api/publish":
			if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
				t.Errorf("decode payload: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := testClient(server).Publish(context.Background(), PublishRequest{
		TrackInfo: TrackInfo{
			ArtistName: "Künstler",
			TrackName:  "Instrumental",
			AlbumName:  "Album",
			Duration:   120 * time.Second,
		},
		Instrumental: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPayload.PlainLyrics != nil || gotPayload.SyncedLyrics != nil {
		t.Fatalf("instrumental payload must not contain lyrics: %+v", gotPayload)
	}
	if gotPayload.TrackName != "Instrumental" || gotPayload.Duration != 120 {
		t.Fatalf("unexpected payload: %+v", gotPayload)
	}
}
