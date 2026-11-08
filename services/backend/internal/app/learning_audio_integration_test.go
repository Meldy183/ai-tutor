package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLearningAudioContractThroughHTTP(t *testing.T) {
	f := newFixture(t)
	student, _, _ := f.register(t, "parent-audio@example.invalid")
	admin := f.admin(t)
	data, err := os.ReadFile("testdata/learning-ready-map.csv")
	if err != nil {
		t.Fatal(err)
	}
	imported := importCourseMap(t, f, "subject:test", data, 0, admin)
	if imported.Code != http.StatusAccepted {
		t.Fatalf("import %d %s", imported.Code, imported.Body.String())
	}
	post := func(path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		r.AddCookie(student)
		w := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(w, r)
		return w
	}
	v := post("/learn/variants", `{"subject_id":"subject:test"}`, "parent-variant")
	var variant struct {
		ID string `json:"id"`
	}
	if v.Code != http.StatusCreated || json.Unmarshal(v.Body.Bytes(), &variant) != nil || variant.ID == "" {
		t.Fatalf("variant %d %s", v.Code, v.Body.String())
	}
	body, _ := json.Marshal(map[string]string{"variant_id": variant.ID})
	start := post("/learn/diagnostic-sessions", string(body), "parent-diagnostic")
	var progress struct {
		ID      string `json:"session_id"`
		Current struct {
			ID string `json:"variant_task_id"`
		} `json:"current"`
	}
	if start.Code != http.StatusCreated || json.Unmarshal(start.Body.Bytes(), &progress) != nil || progress.Current.ID == "" {
		t.Fatalf("diagnostic %d %s", start.Code, start.Body.String())
	}
	var assetID, objectKey string
	if err := f.pool.QueryRow(context.Background(), `SELECT a.id,a.object_key FROM learning_variant_tasks t JOIN learning_audio_assets a ON a.id=t.audio_asset_id WHERE t.id=$1`, progress.Current.ID).Scan(&assetID, &objectKey); err != nil {
		t.Fatal(err)
	}
	f.s3Mu.Lock()
	f.s3Objects[objectKey] = fixtureS3Object{data: wavBytes(), contentType: "audio/wav", assetID: assetID}
	f.s3Mu.Unlock()
	canonical := "/learn/task-audio/" + assetID + "/file"
	for _, prefix := range []string{"/learn/task-audio/"} {
		if _, err := f.pool.Exec(context.Background(), `UPDATE learning_audio_assets SET status='ready',bucket=$2,storage_uri=$3,audio_url=$4 WHERE id=$1`, assetID, f.app.cfg.S3Bucket, "s3://"+f.app.cfg.S3Bucket+"/"+objectKey, prefix+assetID+"/file"); err != nil {
			t.Fatal(err)
		}
		response := f.request(http.MethodGet, "/learn/diagnostic-sessions/"+progress.ID+"/current/audio?variant_task_id="+progress.Current.ID, "", student)
		var metadata struct {
			Status string `json:"status"`
			URL    string `json:"audio_url"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &metadata) != nil || metadata.Status != "ready" || metadata.URL != canonical {
			t.Fatalf("metadata prefix=%s: %d %s", prefix, response.Code, response.Body.String())
		}
		file := f.request(http.MethodGet, metadata.URL, "", student)
		if file.Code != http.StatusOK || file.Header().Get("Content-Type") != "audio/wav" || !bytes.Equal(file.Body.Bytes(), wavBytes()) {
			t.Fatalf("file %d %s", file.Code, file.Body.String())
		}
	}
}
