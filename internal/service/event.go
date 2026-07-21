package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// gcsObject is the payload of a Cloud Storage object-finalize event.
type gcsObject struct {
	Bucket string `json:"bucket"`
	Name   string `json:"name"`
}

// handleEvent processes a GCS object-finalize CloudEvent (Eventarc). It accepts
// the event in structured CloudEvents JSON, binary CloudEvents (data as body),
// or a Pub/Sub push envelope, extracts the finalized object, documents it, and
// writes the result to the configured output bucket.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := readAllLimited(r, 1<<20)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "reading event body: "+err.Error())
		return
	}

	obj, err := decodeGCSEvent(r.Header.Get("Content-Type"), body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !strings.HasSuffix(strings.ToLower(obj.Name), ".qvf") {
		// Not a QVF (or a folder placeholder) — acknowledge so it is not retried.
		s.log.InfoContext(r.Context(), "ignoring non-qvf object", "bucket", obj.Bucket, "name", obj.Name)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "name": obj.Name})
		return
	}
	if s.cfg.OutputBucket == "" {
		s.writeError(w, http.StatusInternalServerError, "no output bucket configured for event mode")
		return
	}

	source := fmt.Sprintf("gs://%s/%s", obj.Bucket, obj.Name)
	resp, err := s.process(r.Context(), ParseRequest{Source: source}, s.cfg.OutputBucket)
	if err != nil {
		s.log.ErrorContext(r.Context(), "event processing failed", "source", source, "error", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.InfoContext(r.Context(), "documented app from event", "source", source, "outputs", resp.Outputs)
	writeJSON(w, http.StatusOK, resp)
}

// decodeGCSEvent extracts the finalized object from the various shapes the event
// may arrive in.
func decodeGCSEvent(contentType string, body []byte) (gcsObject, error) {
	// Pub/Sub push envelope: {"message":{"data":"<base64 gcsObject>"}}.
	var pubsub struct {
		Message struct {
			Data string `json:"data"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &pubsub) == nil && pubsub.Message.Data != "" {
		decoded, err := base64.StdEncoding.DecodeString(pubsub.Message.Data)
		if err != nil {
			return gcsObject{}, fmt.Errorf("decoding pub/sub data: %w", err)
		}
		return unmarshalObject(decoded)
	}

	// Structured CloudEvents JSON: the object is under "data".
	if strings.Contains(contentType, "cloudevents+json") {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
			return unmarshalObject(envelope.Data)
		}
	}

	// Binary CloudEvents / direct payload: the body is the object itself.
	return unmarshalObject(body)
}

func unmarshalObject(data []byte) (gcsObject, error) {
	var obj gcsObject
	if err := json.Unmarshal(data, &obj); err != nil {
		return gcsObject{}, fmt.Errorf("decoding storage object: %w", err)
	}
	if obj.Bucket == "" || obj.Name == "" {
		return gcsObject{}, fmt.Errorf("event missing bucket or name")
	}
	return obj, nil
}

func readAllLimited(r *http.Request, max int64) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, max))
}
