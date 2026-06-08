package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
)

func TestOpenAICompatibleAudioTranscriberPostsMultipartAudio(t *testing.T) {
	t.Parallel()

	const transcriptionModel = "configured-transcription-model"

	var sawAuthorization bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sawAuthorization = r.Header.Get("Authorization") == "Bearer test-key"
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		if got := r.FormValue("model"); got != transcriptionModel {
			t.Fatalf("unexpected model: %s", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("missing file field: %v", err)
		}
		defer file.Close()
		if header.Filename != "voice.wav" {
			t.Fatalf("unexpected filename: %s", header.Filename)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ship the feature"})
	}))
	t.Cleanup(server.Close)

	path := t.TempDir() + "/voice.wav"
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	transcriber := newOpenAICompatibleAudioTranscriber("test-key", server.URL, transcriptionModel, server.Client())
	transcript, err := transcriber.TranscribeAudio(context.Background(), domain.EventAttachment{
		Filename:  "voice.wav",
		LocalPath: path,
	})
	if err != nil {
		t.Fatalf("transcribe audio: %v", err)
	}
	if transcript != "ship the feature" {
		t.Fatalf("unexpected transcript: %s", transcript)
	}
	if !sawAuthorization {
		t.Fatalf("authorization header was not sent")
	}
}

func TestOpenAICompatibleAudioTranscriberConvertsOggAudio(t *testing.T) {
	t.Parallel()

	var convertedInput string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("missing file field: %v", err)
		}
		defer file.Close()
		if header.Filename != "voice.wav" {
			t.Fatalf("unexpected filename: %s", header.Filename)
		}
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read multipart file: %v", err)
		}
		if string(data) != "wav" {
			t.Fatalf("unexpected multipart data: %q", string(data))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ship the feature"})
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	path := dir + "/voice.oga"
	if err := os.WriteFile(path, []byte("ogg"), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	transcriber := newOpenAICompatibleAudioTranscriber("test-key", server.URL, "whisper-1", server.Client())
	transcriber.convert = func(_ context.Context, input string) (string, error) {
		convertedInput = input
		output := dir + "/converted.wav"
		if err := os.WriteFile(output, []byte("wav"), 0o600); err != nil {
			return "", err
		}
		return output, nil
	}
	transcript, err := transcriber.TranscribeAudio(context.Background(), domain.EventAttachment{
		Filename:    "voice.oga",
		ContentType: "audio/ogg",
		LocalPath:   path,
	})
	if err != nil {
		t.Fatalf("transcribe audio: %v", err)
	}
	if transcript != "ship the feature" {
		t.Fatalf("unexpected transcript: %s", transcript)
	}
	if convertedInput != path {
		t.Fatalf("expected converter to receive %q, got %q", path, convertedInput)
	}
}

func TestOpenAICompatibleAudioTranscriberReportsErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad audio", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	path := t.TempDir() + "/voice.wav"
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	transcriber := newOpenAICompatibleAudioTranscriber("test-key", server.URL, "whisper-1", server.Client())
	_, err := transcriber.TranscribeAudio(context.Background(), domain.EventAttachment{
		Filename:  "voice.wav",
		LocalPath: path,
	})
	if err == nil || !strings.Contains(err.Error(), "400 Bad Request") {
		t.Fatalf("expected status error, got %v", err)
	}
}
