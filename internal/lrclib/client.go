// Package lrclib implements the parts of the LRCLIB API that are needed to
// check for existing lyrics and to publish new ones.
package lrclib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"lyrunderground/internal/appinfo"
)

const (
	DefaultBaseURL = "https://lrclib.net"
	UserAgent      = appinfo.NAME + " v0.1.0 (https://github.com/thomue/" + appinfo.NAME + ")"

	defaultMinRequestInterval = 350 * time.Millisecond
	defaultMaxRetries         = 3
)

// ErrNotFound is returned by Check when LRCLIB has no matching track.
var ErrNotFound = errors.New("kein passender Track auf LRCLIB gefunden")

// TrackInfo is the metadata used to find a track on LRCLIB.
type TrackInfo struct {
	ArtistName string
	TrackName  string
	AlbumName  string
	Duration   time.Duration
}

// Record is an LRCLIB lyrics record.
type Record struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	HasWordSync  bool    `json:"hasWordSync"`
	PlainLyrics  *string `json:"plainLyrics"`
	SyncedLyrics *string `json:"syncedLyrics"`
	Lyricsfile   string  `json:"lyricsfile"`
}

// HasLyrics reports whether the record contains actual lyrics. Tracks that are
// marked as instrumental or only exist as an empty metadata record return false.
func (r *Record) HasLyrics() bool {
	if r == nil || r.Instrumental {
		return false
	}
	return nonEmpty(r.PlainLyrics) || nonEmpty(r.SyncedLyrics) || strings.TrimSpace(r.Lyricsfile) != ""
}

func nonEmpty(s *string) bool {
	return s != nil && strings.TrimSpace(*s) != ""
}

// PublishRequest describes a lyrics submission.
type PublishRequest struct {
	TrackInfo
	Lyrics string
	// Synced tells the client to send Lyrics as syncedLyrics instead of
	// plainLyrics.
	Synced bool
	// Instrumental marks the track as instrumental. No lyrics fields are sent
	// in this case.
	Instrumental bool
}

// APIError is an error response returned by LRCLIB.
type APIError struct {
	StatusCode int
	Name       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" && e.Name != "" {
		return fmt.Sprintf("LRCLIB: %s (%s, HTTP %d)", e.Message, e.Name, e.StatusCode)
	}
	if e.Message != "" {
		return fmt.Sprintf("LRCLIB: %s (HTTP %d)", e.Message, e.StatusCode)
	}
	return fmt.Sprintf("LRCLIB: HTTP %d", e.StatusCode)
}

// Client is a rate-limit-aware LRCLIB API client. Requests are serialized and a
// small delay is inserted between them, as requested by the API documentation.
type Client struct {
	BaseURL            string
	HTTPClient         *http.Client
	UserAgent          string
	MinRequestInterval time.Duration
	MaxRetries         int

	mu          sync.Mutex
	lastRequest time.Time
}

// NewClient creates a client using the public LRCLIB instance.
func NewClient() *Client {
	return &Client{
		BaseURL:            DefaultBaseURL,
		HTTPClient:         &http.Client{Timeout: 60 * time.Second},
		UserAgent:          UserAgent,
		MinRequestInterval: defaultMinRequestInterval,
		MaxRetries:         defaultMaxRetries,
	}
}

// Check looks up the best matching lyrics record. It returns ErrNotFound when
// the API responds with 404. If an album-specific lookup fails, it retries
// without the album filter to avoid false negatives when LRCLIB's album
// normalization differs from the local tag.
func (c *Client) Check(ctx context.Context, info TrackInfo) (*Record, error) {
	if strings.TrimSpace(info.ArtistName) == "" || strings.TrimSpace(info.TrackName) == "" {
		return nil, errors.New("Artist und Titel werden für die Suche benötigt")
	}

	record, err := c.checkOnce(ctx, info, true)
	if errors.Is(err, ErrNotFound) && strings.TrimSpace(info.AlbumName) != "" {
		return c.checkOnce(ctx, info, false)
	}
	return record, err
}

func (c *Client) checkOnce(ctx context.Context, info TrackInfo, withAlbum bool) (*Record, error) {
	query := url.Values{}
	query.Set("track_name", info.TrackName)
	query.Set("artist_name", info.ArtistName)
	if withAlbum && strings.TrimSpace(info.AlbumName) != "" {
		query.Set("album_name", info.AlbumName)
	}
	if info.Duration > 0 {
		seconds := int(math.Round(info.Duration.Seconds()))
		// /api/get rejects durations outside 1..3600. Omitting the value is
		// better than failing for unusually long tracks.
		if seconds >= 1 && seconds <= 3600 {
			query.Set("duration", strconv.Itoa(seconds))
		}
	}

	body, status, err := c.do(ctx, http.MethodGet, "/api/get", query, nil, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusOK:
		var record Record
		if err := json.Unmarshal(body, &record); err != nil {
			return nil, fmt.Errorf("ungültige LRCLIB-Antwort: %w", err)
		}
		return &record, nil
	case status == http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, parseAPIError(status, body)
	}
}

// Publish solves a fresh proof-of-work challenge and submits the lyrics.
func (c *Client) Publish(ctx context.Context, req PublishRequest) error {
	lyrics := strings.TrimSpace(req.Lyrics)
	if lyrics == "" && !req.Instrumental {
		return errors.New("keine Lyrics zum Hochladen angegeben")
	}
	if strings.TrimSpace(req.ArtistName) == "" || strings.TrimSpace(req.TrackName) == "" {
		return errors.New("Artist und Titel werden für den Upload benötigt")
	}

	challenge, err := c.requestChallenge(ctx)
	if err != nil {
		return err
	}
	nonce, err := SolveChallenge(ctx, challenge.Prefix, challenge.Target)
	if err != nil {
		return fmt.Errorf("Proof-of-Work konnte nicht gelöst werden: %w", err)
	}
	token := challenge.Prefix + ":" + nonce

	payload := publishPayload{
		TrackName:  req.TrackName,
		ArtistName: req.ArtistName,
		AlbumName:  req.AlbumName,
		Duration:   math.Round(req.Duration.Seconds()),
	}
	if !req.Instrumental {
		if req.Synced {
			payload.SyncedLyrics = &lyrics
		} else {
			payload.PlainLyrics = &lyrics
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	headers := map[string]string{"X-Publish-Token": token}
	body, status, err := c.do(ctx, http.MethodPost, "/api/publish", nil, data, headers)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return parseAPIError(status, body)
	}
	return nil
}

type publishPayload struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	PlainLyrics  *string `json:"plainLyrics,omitempty"`
	SyncedLyrics *string `json:"syncedLyrics,omitempty"`
}

type challenge struct {
	Prefix string `json:"prefix"`
	Target string `json:"target"`
}

func (c *Client) requestChallenge(ctx context.Context) (challenge, error) {
	body, status, err := c.do(ctx, http.MethodPost, "/api/request-challenge", nil, nil, nil)
	if err != nil {
		return challenge{}, err
	}
	if status < 200 || status >= 300 {
		return challenge{}, parseAPIError(status, body)
	}
	var ch challenge
	if err := json.Unmarshal(body, &ch); err != nil {
		return challenge{}, fmt.Errorf("ungültige Challenge-Antwort: %w", err)
	}
	if ch.Prefix == "" || ch.Target == "" {
		return challenge{}, errors.New("LRCLIB hat eine unvollständige Challenge geliefert")
	}
	return ch, nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, query url.Values, body []byte, headers map[string]string) ([]byte, int, error) {
	endpoint = strings.TrimRight(c.BaseURL, "/") + endpoint
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if err := c.waitLocked(ctx); err != nil {
			return nil, 0, err
		}

		req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		c.lastRequest = time.Now()
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			// A temporary network error is worth one retry, but not a tight loop.
			if attempt < c.MaxRetries {
				if err := sleepContext(ctx, time.Second); err != nil {
					return nil, 0, err
				}
				continue
			}
			return nil, 0, err
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, readErr
		}

		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) &&
			attempt < c.MaxRetries {
			delay := retryAfter(resp.Header.Get("Retry-After"), resp.StatusCode)
			delay += time.Duration(rand.Float64() * 0.3 * float64(delay))
			if err := sleepContext(ctx, delay); err != nil {
				return nil, resp.StatusCode, err
			}
			lastErr = parseAPIError(resp.StatusCode, data)
			continue
		}
		return data, resp.StatusCode, nil
	}
	if lastErr != nil {
		return nil, 0, lastErr
	}
	return nil, 0, errors.New("LRCLIB-Anfrage fehlgeschlagen")
}

func (c *Client) waitLocked(ctx context.Context) error {
	interval := c.MinRequestInterval
	if interval < 0 {
		interval = 0
	}
	if c.lastRequest.IsZero() {
		return nil
	}
	delay := interval - time.Since(c.lastRequest)
	if delay <= 0 {
		return nil
	}
	return sleepContext(ctx, delay)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfter(value string, status int) time.Duration {
	value = strings.TrimSpace(value)
	if value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if t, err := http.ParseTime(value); err == nil {
			if delay := time.Until(t); delay > 0 {
				return delay
			}
		}
	}
	if status == http.StatusServiceUnavailable {
		return time.Second
	}
	return 2 * time.Second
}

func parseAPIError(status int, body []byte) error {
	var response struct {
		Message    string `json:"message"`
		Name       string `json:"name"`
		StatusCode int    `json:"statusCode"`
	}
	if err := json.Unmarshal(body, &response); err == nil && (response.Message != "" || response.Name != "") {
		code := response.StatusCode
		if code == 0 {
			code = status
		}
		return &APIError{StatusCode: code, Name: response.Name, Message: response.Message}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return &APIError{StatusCode: status, Message: text}
}
