package notes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestReadJSONFeed(t *testing.T) {
	path := writeTemp(t, "feed.json", `{
	  "title": "prep",
	  "items": [
	    {"id":"n-1","title":"is it frontmatter?","kind":"question"},
	    {"id":"n-2","title":"frontmatter is not indexed","kind":"gap","body":"37/39"}
	  ]
	}`)
	feed, err := Read(path, nil, time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if feed.Title != "prep" || len(feed.Items) != 2 {
		t.Fatalf("unexpected feed: %+v", feed)
	}
	if feed.Items[1].Kind != "gap" || feed.Items[1].Body != "37/39" {
		t.Fatalf("item not parsed: %+v", feed.Items[1])
	}
}

func TestReadBareJSONArray(t *testing.T) {
	path := writeTemp(t, "bare.json", `[{"id":"n-1","title":"one"}]`)
	feed, err := Read(path, nil, time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(feed.Items) != 1 || feed.Items[0].ID != "n-1" {
		t.Fatalf("unexpected feed: %+v", feed)
	}
}

func TestReadRSS(t *testing.T) {
	path := writeTemp(t, "feed.xml", `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>prep</title>
  <item>
    <title>is it frontmatter?</title>
    <guid>n-1</guid>
    <link>http://example/n-1</link>
    <pubDate>Mon, 27 Oct 2026 06:00:00 GMT</pubDate>
    <description>read the file</description>
    <category>question</category>
  </item>
</channel></rss>`)
	feed, err := Read(path, nil, time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("expected one item, got %+v", feed)
	}
	it := feed.Items[0]
	if it.ID != "n-1" || it.Kind != "question" || it.Body != "read the file" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.Published.IsZero() {
		t.Fatalf("pubDate not parsed: %+v", it)
	}
}

func TestStoreCaches(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"items":[{"id":"n-1","title":"one"}]}`))
	}))
	defer srv.Close()

	store := NewStore(time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := store.Get("prep", srv.URL); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected one fetch, got %d", got)
	}
}

func TestReadMissingSource(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.json"), nil, time.Second); err == nil {
		t.Fatal("expected an error for a missing source")
	}
}
