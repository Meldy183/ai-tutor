package app

import (
	"net/http"
	"strings"

	graphhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/transport/http"
)

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		limit := int64(32 * 1024)
		if r.Method == http.MethodPost && isLearningAudioUpload(r.URL.Path) {
			limit = a.cfg.MaxUploadBytes + 64*1024
		} else if r.Method == http.MethodPost && r.URL.Path == "/learn/admin/materials" {
			// JSON may encode one supplementary Unicode scalar as a surrogate
			// pair (12 wire bytes), so size for the worst valid 100,000-rune body.
			limit = 12*100000 + 64*1024
		} else if strings.HasPrefix(r.URL.Path, "/exam/admin/subjects/") && strings.HasSuffix(r.URL.Path, "/materials") {
			limit = 2500 * 1024 * 1024
		} else if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/exam/admin/subjects/") && strings.HasSuffix(r.URL.Path, "/raw") {
			limit = 51 * 1024 * 1024
		} else if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/exam/admin/subjects/") && strings.HasSuffix(r.URL.Path, "/graph/competency-map") {
			// The handler bounds the book itself and answers 413 above it.
			limit = graphhttp.MaxMapBytes + 2<<20
		} else if r.Method == http.MethodPost && r.URL.Path == "/voice/transcriptions" {
			limit = a.cfg.MaxUploadBytes + 64*1024
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func isLearningAudioUpload(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 4 && parts[0] == "learn" && parts[3] == "answers" {
		return parts[2] != "" && (parts[1] == "diagnostic-sessions" || parts[1] == "training-sessions" || parts[1] == "learning-plans")
	}
	return len(parts) == 7 && parts[0] == "learn" && parts[1] == "training-sessions" && parts[3] == "exercises" && parts[5] == "chat" && parts[6] == "turns"
}
