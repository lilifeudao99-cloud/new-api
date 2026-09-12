package common

import "testing"

func TestRewriteExternalErrorURLs(t *testing.T) {
	message := `failed: https://upstream.example/v1/videos/task-1?download=1#result; keep https://api.ailili.chat/v1/models`
	got := RewriteExternalErrorURLs(message, "ailili.chat", "https")
	want := `failed: https://ailili.chat/v1/videos/task-1?download=1#result; keep https://api.ailili.chat/v1/models`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
