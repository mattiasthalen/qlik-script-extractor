package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
)

// Storage resolves and reads/writes objects addressed by URI. It supports
// gs:// (Google Cloud Storage), http(s):// (e.g. signed URLs, download only)
// and local filesystem paths (plain or file://). The GCS client is created
// lazily so the service can run and be tested without credentials when only
// local or HTTP inputs are used.
type Storage struct {
	mu   sync.Mutex
	gcs  *storage.Client
	http *http.Client
}

// NewStorage returns a Storage with a sane default HTTP client.
func NewStorage() *Storage {
	return &Storage{http: &http.Client{Timeout: 0}}
}

// Close releases the GCS client if one was created.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gcs != nil {
		err := s.gcs.Close()
		s.gcs = nil
		return err
	}
	return nil
}

// Materialize returns a local filesystem path for uri. Remote objects (gs://,
// http(s)://) are streamed to a new temp file under tmpDir and isTemp is true,
// signalling the caller to remove it when done. Local inputs are returned in
// place with isTemp false.
func (s *Storage) Materialize(ctx context.Context, uri, tmpDir string) (path string, isTemp bool, err error) {
	switch {
	case strings.HasPrefix(uri, "gs://"):
		p, err := s.downloadGCS(ctx, uri, tmpDir)
		return p, true, err
	case strings.HasPrefix(uri, "http://"), strings.HasPrefix(uri, "https://"):
		p, err := s.downloadHTTP(ctx, uri, tmpDir)
		return p, true, err
	default:
		local := strings.TrimPrefix(uri, "file://")
		if _, err := os.Stat(local); err != nil {
			return "", false, fmt.Errorf("local input %q: %w", local, err)
		}
		return local, false, nil
	}
}

// Upload writes data to uri. gs:// and local/file:// destinations are
// supported; http(s):// is not.
func (s *Storage) Upload(ctx context.Context, uri string, data []byte, contentType string) error {
	switch {
	case strings.HasPrefix(uri, "gs://"):
		return s.uploadGCS(ctx, uri, data, contentType)
	case strings.HasPrefix(uri, "http://"), strings.HasPrefix(uri, "https://"):
		return fmt.Errorf("upload to an http(s) URL is not supported: %s", uri)
	default:
		local := strings.TrimPrefix(uri, "file://")
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return err
		}
		return os.WriteFile(local, data, 0o644)
	}
}

func (s *Storage) gcsClient(ctx context.Context) (*storage.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gcs == nil {
		c, err := storage.NewClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("creating GCS client: %w", err)
		}
		s.gcs = c
	}
	return s.gcs, nil
}

func (s *Storage) downloadGCS(ctx context.Context, uri, tmpDir string) (string, error) {
	bucket, object, err := parseGSURI(uri)
	if err != nil {
		return "", err
	}
	client, err := s.gcsClient(ctx)
	if err != nil {
		return "", err
	}
	r, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return "", fmt.Errorf("opening gs://%s/%s: %w", bucket, object, err)
	}
	defer func() { _ = r.Close() }()
	return streamToTemp(r, tmpDir, filepath.Base(object))
}

func (s *Storage) downloadHTTP(ctx context.Context, uri, tmpDir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", uri, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: unexpected status %s", uri, resp.Status)
	}
	name := filepath.Base(strings.SplitN(uri, "?", 2)[0])
	if name == "" || name == "/" || name == "." {
		name = "download.qvf"
	}
	return streamToTemp(resp.Body, tmpDir, name)
}

func (s *Storage) uploadGCS(ctx context.Context, uri string, data []byte, contentType string) error {
	bucket, object, err := parseGSURI(uri)
	if err != nil {
		return err
	}
	client, err := s.gcsClient(ctx)
	if err != nil {
		return err
	}
	w := client.Bucket(bucket).Object(object).NewWriter(ctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("writing gs://%s/%s: %w", bucket, object, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing gs://%s/%s: %w", bucket, object, err)
	}
	return nil
}

// streamToTemp copies r into a uniquely named temp file under tmpDir, streaming
// so the payload never has to fit in memory.
func streamToTemp(r io.Reader, tmpDir, hint string) (string, error) {
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(tmpDir, fmt.Sprintf("qvf-%d-*-%s", time.Now().UnixNano(), sanitize(hint)))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("streaming to temp file: %w", err)
	}
	return f.Name(), nil
}

// parseGSURI splits gs://bucket/object into its parts.
func parseGSURI(uri string) (bucket, object string, err error) {
	rest := strings.TrimPrefix(uri, "gs://")
	if rest == uri {
		return "", "", fmt.Errorf("not a gs:// URI: %s", uri)
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed gs:// URI: %s", uri)
	}
	return parts[0], parts[1], nil
}

func sanitize(name string) string {
	name = filepath.Base(name)
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
}
