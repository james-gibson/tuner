// Package notes implements the notes channel: a small, RSS-shaped feed of the
// questions and gaps worth surfacing on the live channel.
//
// It is deliberately a feed and not a store. The notes themselves live where they
// are authored (a JSONL file, a Qdrant collection); tuner's job is to surface the
// latest ones to whoever is watching. That keeps the channel ephemeral, which is
// what its signal decay already assumes, and keeps the content out of tuner.
package notes

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Item is one note as the channel presents it.
type Item struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind,omitempty"` // question | gap | rule | tool
	Body      string    `json:"body,omitempty"`
	Link      string    `json:"link,omitempty"`
	Published time.Time `json:"published,omitempty"`
}

// Feed is the channel's current content.
type Feed struct {
	Title   string    `json:"title"`
	Items   []Item    `json:"items"`
	Fetched time.Time `json:"fetched"`
}

// rssDoc covers RSS 2.0; Atom's <entry> is normalised separately.
type rssDoc struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			GUID        string `xml:"guid"`
			PubDate     string `xml:"pubDate"`
			Description string `xml:"description"`
			Category    string `xml:"category"`
		} `xml:"item"`
	} `xml:"channel"`
	// Atom
	Title   string `xml:"title"`
	Entries []struct {
		Title   string `xml:"title"`
		ID      string `xml:"id"`
		Updated string `xml:"updated"`
		Summary string `xml:"summary"`
		Link    struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

// Read loads a feed from a path (plain or file://) or an http(s) URL.
func Read(source string, client *http.Client, timeout time.Duration) (Feed, error) {
	body, err := load(source, client, timeout)
	if err != nil {
		return Feed{}, err
	}
	return parse(body)
}

func load(source string, client *http.Client, timeout time.Duration) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		if client == nil {
			client = &http.Client{Timeout: timeout}
		}
		resp, err := client.Get(source) //nolint:noctx // the client carries the timeout
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("notes: %s returned %d", source, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}
	path := strings.TrimPrefix(source, "file://")
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	// The feed path is operator-supplied config, the same trust as the shell.
	return os.ReadFile(path) //nolint:gosec // G304: operator-supplied feed path by design
}

func parse(body []byte) (Feed, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "<") {
		var doc rssDoc
		if err := xml.Unmarshal(body, &doc); err != nil {
			return Feed{}, fmt.Errorf("notes: parse xml: %w", err)
		}
		return fromXML(doc), nil
	}
	// JSON: either {"title","items"} or a bare array of items.
	var wrapped struct {
		Title string `json:"title"`
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && (wrapped.Title != "" || wrapped.Items != nil) {
		if wrapped.Items == nil {
			wrapped.Items = []Item{}
		}
		return Feed{Title: wrapped.Title, Items: wrapped.Items, Fetched: time.Now().UTC()}, nil
	}
	var items []Item
	if err := json.Unmarshal(body, &items); err != nil {
		return Feed{}, fmt.Errorf("notes: parse json: %w", err)
	}
	return Feed{Items: items, Fetched: time.Now().UTC()}, nil
}

func fromXML(doc rssDoc) Feed {
	feed := Feed{Fetched: time.Now().UTC()}
	if doc.Channel.Title != "" {
		feed.Title = doc.Channel.Title
		for _, it := range doc.Channel.Items {
			id := it.GUID
			if id == "" {
				id = it.Link
			}
			feed.Items = append(feed.Items, Item{
				ID:        id,
				Title:     it.Title,
				Kind:      it.Category,
				Body:      it.Description,
				Link:      it.Link,
				Published: parseTime(it.PubDate),
			})
		}
		return feed
	}
	// Atom
	feed.Title = doc.Title
	for _, e := range doc.Entries {
		feed.Items = append(feed.Items, Item{
			ID:        e.ID,
			Title:     e.Title,
			Body:      e.Summary,
			Link:      e.Link.Href,
			Published: parseTime(e.Updated),
		})
	}
	return feed
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 MST"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// Store caches one feed per channel so a watching client cannot hammer the source.
type Store struct {
	mu    sync.Mutex
	cache map[string]cached
	ttl   time.Duration
}

type cached struct {
	feed Feed
	at   time.Time
}

// NewStore returns a store with the given per-feed cache lifetime.
func NewStore(ttl time.Duration) *Store {
	return &Store{cache: map[string]cached{}, ttl: ttl}
}

// Get returns a fresh-enough feed for the named channel.
func (s *Store) Get(name, source string) (Feed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[name]; ok && time.Since(c.at) < s.ttl {
		return c.feed, nil
	}
	feed, err := Read(source, nil, 5*time.Second)
	if err != nil {
		return Feed{}, err
	}
	s.cache[name] = cached{feed: feed, at: time.Now()}
	return feed, nil
}
