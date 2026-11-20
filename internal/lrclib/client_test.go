package lrclib

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestCheckFallsBackWithoutAlbum(t *testing.T) {
	var albumFilters []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		albumFilters = append(albumFilters, r.URL.Query().Get("album_name"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("album_name") != "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Failed to find specified track","name":"TrackNotFound","statusCode":404}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":42,"trackName":"Titel","artistName":"Künstler","albumName":"Album","duration":196,"instrumental":false,"plainLyrics":"Zeile","syncedLyrics":null,"lyricsfile":""}`)
	}))
	defer server.Close()

	record, err := testClient(server).Check(context.Background(), TrackInfo{
		ArtistName: "Künstler",
		TrackName:  "Titel",
		AlbumName:  "Album",
		Duration:   196 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != 42 {
		t.Fatalf("unexpected record: %+v", record)
	}
	if len(albumFilters) != 2 {
		t.Fatalf("got %d requests, want 2 (with and without album)", len(albumFilters))
	}
	if albumFilters[0] != "Album" || albumFilters[1] != "" {
		t.Fatalf("unexpected album filters: %#v", albumFilters)
	}
}

// failingTransport simulates a connection that never reaches the server.
type failingTransport struct {
	calls atomic.Int32
}

func (t *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("network unreachable")
}

func newFailingClient() (*Client, *failingTransport) {
	transport := &failingTransport{}
	return &Client{
		BaseURL:            "https://lrclib.invalid",
		HTTPClient:         &http.Client{Transport: transport},
		UserAgent:          appinfo.NAME + "-test",
		MinRequestInterval: 0,
		MaxRetries:         1,
	}, transport
}

func TestNetworkErrorRetriesIdempotentRequests(t *testing.T) {
	client, transport := newFailingClient()

	_, err := client.Check(context.Background(), TrackInfo{ArtistName: "A", TrackName: "T"})
	if err == nil {
		t.Fatal("expected network error")
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("GET was sent %d times, want 2 (initial request plus one retry)", got)
	}
}

func TestNetworkErrorDoesNotRetryPublish(t *testing.T) {
	client, transport := newFailingClient()

	err := client.Publish(context.Background(), PublishRequest{
		TrackInfo: TrackInfo{ArtistName: "A", TrackName: "T", Duration: 100 * time.Second},
		Lyrics:    "Zeile",
	})
	if err == nil {
		t.Fatal("expected network error")
	}
	// The challenge request is a POST as well. Retrying it after a network
	// error is safe, but the test asserts that the failing POST path is not
	// silently repeated, which is what would burn a single-use publish token.
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("failing POST was sent %d times, want 1", got)
	}
}

func TestPublishAlwaysSendsAlbumName(t *testing.T) {
	var raw map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request-challenge":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"prefix":"abc123","target":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`)
		case "/api/publish":
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("decode payload: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// LRCLIB deserialises albumName into a required string field, so omitting
	// it for tracks without an album tag would break the upload.
	err := testClient(server).Publish(context.Background(), PublishRequest{
		TrackInfo: TrackInfo{ArtistName: "Künstler", TrackName: "Titel", Duration: 100 * time.Second},
		Lyrics:    "Zeile",
	})
	if err != nil {
		t.Fatal(err)
	}
	album, ok := raw["albumName"]
	if !ok {
		t.Fatal("publish payload must always contain albumName")
	}
	if album != "" {
		t.Fatalf("albumName = %v, want an empty string", album)
	}
}

func TestUserAgentFollowsBuildVersion(t *testing.T) {
	// The default User-Agent must carry the build version instead of a
	// hard-coded one, otherwise every release reports the wrong version to
	// LRCLIB.
	if !strings.Contains(UserAgent, appinfo.Version()) {
		t.Fatalf("UserAgent %q does not contain the build version %q", UserAgent, appinfo.Version())
	}
	if !strings.HasPrefix(UserAgent, appinfo.NAME+" ") {
		t.Fatalf("UserAgent %q does not start with the application name", UserAgent)
	}
	if !strings.Contains(UserAgent, "https://") && !strings.Contains(UserAgent, "@") {
		t.Fatalf("UserAgent %q has no project link or e-mail address", UserAgent)
	}
}

func TestHasLyricsIgnoresLyricsfile(t *testing.T) {
	// LRCLIB attaches a lyricsfile to every record, so a record with only that
	// field must not count as "has lyrics".
	record := &Record{ID: 1, Lyricsfile: "track:\n  title: T\n"}
	if record.HasLyrics() {
		t.Fatal("record without plain or synced lyrics must not report lyrics")
	}

	plain := "Zeile"
	record = &Record{ID: 1, PlainLyrics: &plain, Lyricsfile: "track:\n  title: T\n"}
	if !record.HasLyrics() {
		t.Fatal("record with plain lyrics must report lyrics")
	}

	instrumental := &Record{ID: 1, Instrumental: true, Lyricsfile: "track:\n  title: T\n"}
	if instrumental.HasLyrics() {
		t.Fatal("instrumental record must not report lyrics")
	}
}

func TestPublishRetriesWithFreshChallenge(t *testing.T) {
	var challenges, publishes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request-challenge":
			challenges++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"prefix":"abc123","target":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`)
		case "/api/publish":
			publishes++
			if publishes == 1 {
				// Expired challenge / already used token.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"message":"The provided publish token is incorrect","name":"IncorrectPublishTokenError","statusCode":400}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := testClient(server).Publish(context.Background(), PublishRequest{
		TrackInfo: TrackInfo{ArtistName: "Künstler", TrackName: "Titel", Duration: 100 * time.Second},
		Lyrics:    "Zeile",
	})
	if err != nil {
		t.Fatalf("publish did not recover from an expired token: %v", err)
	}
	if publishes != 2 {
		t.Fatalf("publish was sent %d times, want 2", publishes)
	}
	if challenges != 2 {
		t.Fatalf("requested %d challenges, want 2 (one per attempt)", challenges)
	}
}

func TestPublishGivesUpAfterOneTokenRetry(t *testing.T) {
	var challenges, publishes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request-challenge":
			challenges++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"prefix":"abc123","target":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`)
		case "/api/publish":
			publishes++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"The provided publish token is incorrect","name":"IncorrectPublishTokenError","statusCode":400}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := testClient(server).Publish(context.Background(), PublishRequest{
		TrackInfo: TrackInfo{ArtistName: "Künstler", TrackName: "Titel", Duration: 100 * time.Second},
		Lyrics:    "Zeile",
	})
	if err == nil {
		t.Fatal("expected an error when every token is rejected")
	}
	if publishes != 2 || challenges != 2 {
		t.Fatalf("publishes=%d challenges=%d, want exactly one retry (2/2)", publishes, challenges)
	}
}
