package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/AskarKasimov/ai-tutor/services/audio"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/httpx"
)

// Compile the complete contract, including planned schemas, without a database.
func TestOpenAPIContractSchemas(t *testing.T) {
	data, err := os.ReadFile("../../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	const resource = "https://tutor.example/openapi.json"
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(resource, doc); err != nil {
		t.Fatal(err)
	}
	for name := range spec["components"].(map[string]any)["schemas"].(map[string]any) {
		if _, err := compiler.Compile(resource + "#/components/schemas/" + name); err != nil {
			t.Errorf("invalid schema %s: %v", name, err)
		}
	}
	for path, node := range spec["paths"].(map[string]any) {
		for method, value := range node.(map[string]any) {
			if !strings.Contains(" get post put patch delete head options ", " "+method+" ") {
				continue
			}
			status := value.(map[string]any)["x-implementation"]
			if status != "implemented" && status != "planned" {
				t.Errorf("%s %s requires explicit implementation status", method, path)
			}
		}
	}
}

// Validate actual handler responses using only implemented contract operations.
func TestOpenAPIResponses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	data, err := os.ReadFile("../../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err = yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const resource = "https://tutor.example/openapi.json"
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err = json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	if err = compiler.AddResource(resource, doc); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	checked := map[string]bool{}
	check := func(method, path string, w *httptest.ResponseRecorder) {
		t.Helper()
		contractPath := path
		if strings.HasPrefix(path, "/admin/") || path == "/subjects" || strings.HasPrefix(path, "/subjects/") || strings.HasPrefix(path, "/diagnostic-sessions") {
			contractPath = "/exam" + path
		}
		op := paths[contractPath].(map[string]any)[strings.ToLower(method)].(map[string]any)
		response, ok := op["responses"].(map[string]any)[fmt.Sprint(w.Code)]
		if !ok {
			t.Fatalf("response status not in contract: %s %s %d %s", method, path, w.Code, w.Body.String())
		}
		node := response.(map[string]any)
		if ref, ok := node["$ref"].(string); ok {
			node = spec["components"].(map[string]any)["responses"].(map[string]any)[strings.TrimPrefix(ref, "#/components/responses/")].(map[string]any)
		}
		if w.Code == 204 {
			if w.Body.Len() != 0 {
				t.Fatal("204 response has body")
			}
			checked[contractPath] = true
			return
		}
		content := node["content"].(map[string]any)
		if _, ok := content["audio/wav"]; ok {
			if w.Header().Get("Content-Type") != "audio/wav" || !audio.ValidWAV(w.Body.Bytes()) {
				t.Fatal("invalid WAV response")
			}
			checked[contractPath] = true
			return
		}
		if _, ok := content["application/octet-stream"]; ok {
			if w.Body.Len() == 0 {
				t.Fatal("empty octet-stream response")
			}
			checked[contractPath] = true
			return
		}
		if _, ok := content["application/pdf"]; ok {
			if w.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
				t.Fatal("invalid PDF response")
			}
			checked[contractPath] = true
			return
		}
		schemaNode := content["application/json"].(map[string]any)["schema"].(map[string]any)
		ref, ok := schemaNode["$ref"].(string)
		if !ok {
			escape := strings.NewReplacer("~", "~0", "/", "~1")
			ref = "#/paths/" + escape.Replace(contractPath) + "/" + strings.ToLower(method) +
				"/responses/" + fmt.Sprint(w.Code) + "/content/application~1json/schema"
		}
		schema, err := compiler.Compile(resource + ref)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err = json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if err = schema.Validate(value); err != nil {
			t.Fatalf("contract mismatch %s %d: %v", path, w.Code, err)
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("wrong response type")
		}
		if w.Code < 300 {
			checked[contractPath] = true
		}
	}
	register := f.request("POST", "/auth/register", `{"email":"contract@example.edu","password":"`+password+`","display_name":"Контракт"}`)
	check("POST", "/auth/register", register)
	cookies := register.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal("missing cookies")
	}
	access, refresh := cookies[0], cookies[1]
	check("GET", "/auth/me", f.request("GET", "/auth/me", "", access))
	check("GET", "/auth/me", f.request("GET", "/auth/me", ""))
	check("POST", "/auth/login", f.request("POST", "/auth/login", `{"email":"contract@example.edu","password":"`+password+`"}`))
	check("POST", "/auth/teacher", f.portal("POST", "/auth/teacher", ""))
	check("POST", "/auth/teacher", f.request("POST", "/auth/teacher", ""))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/synthesize":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(wavBytes())
		case "/transcribe":
			httpx.JSON(w, 200, map[string]any{"text": "Ответ.", "model": "v3", "segments": []any{}})
		default:
			t.Errorf("unexpected provider path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	f.voice.SetSTTURL(provider.URL + "/transcribe")
	f.voice.SetTTSURL(provider.URL + "/synthesize")
	transcription := upload(f, "/voice/transcriptions", "audio", "answer.wav", "audio/wav", wavBytes(), access)
	check("POST", "/voice/transcriptions", transcription)
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(transcription.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if transcription.Code != http.StatusOK || saved.ID == "" {
		t.Fatalf("transcription: %d %s", transcription.Code, transcription.Body.String())
	}
	check("POST", "/voice/syntheses", f.request("POST", "/voice/syntheses", `{"text":"Вопрос?"}`, access))
	check("POST", "/voice/syntheses", f.request("POST", "/voice/syntheses", `{"text":""}`, access))
	admin := f.admin(t)
	createdSubject := f.request("POST", "/admin/subjects", `{"name":"Предмет контракта"}`, admin)
	check("POST", "/admin/subjects", createdSubject)
	var contractSubject struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdSubject.Body.Bytes(), &contractSubject); err != nil || contractSubject.ID == "" {
		t.Fatalf("created subject: %s", createdSubject.Body.String())
	}
	// This fixture does not start background workers, including audio recovery.
	f.app.warmer.MarkRecoveryComplete()
	audioPreparation := f.request("GET", "/admin/subjects/"+contractSubject.ID+"/audio-preparation?direction=learn", "", admin)
	if audioPreparation.Code != http.StatusOK {
		t.Fatalf("audio preparation: %d %s", audioPreparation.Code, audioPreparation.Body.String())
	}
	check("GET", "/admin/subjects/{subject_id}/audio-preparation", audioPreparation)
	examAudio := f.request("GET", "/admin/subjects/"+contractSubject.ID+"/audio-preparation?direction=exam", "", admin)
	if examAudio.Code != http.StatusOK {
		t.Fatalf("exam audio preparation: %d %s", examAudio.Code, examAudio.Body.String())
	}
	check("GET", "/admin/subjects/{subject_id}/audio-preparation", examAudio)
	invalidAudio := f.request("GET", "/admin/subjects/"+contractSubject.ID+"/audio-preparation", "", admin)
	if invalidAudio.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing audio direction: %d %s", invalidAudio.Code, invalidAudio.Body.String())
	}
	check("GET", "/admin/subjects/{subject_id}/audio-preparation", invalidAudio)
	mapPath := "/admin/subjects/" + contractSubject.ID + "/graph/competency-map"
	const mapContract = "/admin/subjects/{subject_id}/graph/competency-map"
	const skipContract = "/admin/subjects/{subject_id}/graph/competency-map/skip-missing-criteria"
	beforeImport := f.request("POST", mapPath+"/skip-missing-criteria", "", admin)
	if beforeImport.Code != http.StatusUnprocessableEntity {
		t.Fatalf("skip before import: %d %s", beforeImport.Code, beforeImport.Body.String())
	}
	check("POST", skipContract, beforeImport)
	notABook := upload(f, mapPath, "file", "map.pdf", "application/pdf", []byte("%PDF-1.7"), admin)
	if notABook.Code != http.StatusUnprocessableEntity {
		t.Fatalf("map that is not a book: %d %s", notABook.Code, notABook.Body.String())
	}
	check("POST", mapContract, notABook)
	imported := uploadWithHeader(f, mapPath, "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", mapBook(t), admin, "X-Expected-Learning-Revision", "0")
	if imported.Code != http.StatusAccepted || !strings.Contains(imported.Body.String(), `"build_source":"competency_map"`) {
		t.Fatalf("map import: %d %s", imported.Code, imported.Body.String())
	}
	check("POST", mapContract, imported)
	whileBuilding := f.request("POST", mapPath+"/skip-missing-criteria", "", admin)
	if whileBuilding.Code != http.StatusConflict {
		t.Fatalf("skip while building: %d %s", whileBuilding.Code, whileBuilding.Body.String())
	}
	check("POST", skipContract, whileBuilding)
	check("POST", mapContract, uploadWithHeader(f, mapPath, "file", "map.xlsx", "application/octet-stream", mapBook(t), admin, "X-Expected-Learning-Revision", "1"))
	// Publish the imported graph as the worker would, then validate the 202
	// response of the actual skip endpoint against the OpenAPI snapshot schema.
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO course_graphs(subject_id, source, fingerprint, graph, updated_at) SELECT subject_id, 'competency_map', fingerprint, graph, 1 FROM course_graph_maps WHERE subject_id=$1`, contractSubject.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE course_graph_jobs SET status='ready' WHERE subject_id=$1`, contractSubject.ID); err != nil {
		t.Fatal(err)
	}
	missingConfirmation := f.request("POST", mapPath+"/skip-missing-criteria", "", admin)
	if missingConfirmation.Code != http.StatusConflict || !strings.Contains(missingConfirmation.Body.String(), "COMPETENCY_MAP_CHANGED") {
		t.Fatalf("skip without confirmation: %d %s", missingConfirmation.Code, missingConfirmation.Body.String())
	}
	check("POST", skipContract, missingConfirmation)
	var importedSnapshot struct {
		Map struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"map"`
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &importedSnapshot); err != nil || importedSnapshot.Map.Fingerprint == "" {
		t.Fatalf("missing imported map fingerprint: %s (%v)", imported.Body.String(), err)
	}
	confirmedRequest := func(fingerprint string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://api.example"+examTestPath(mapPath+"/skip-missing-criteria"), nil)
		req.AddCookie(admin)
		req.Header.Set("X-Expected-Map-Fingerprint", fingerprint)
		w := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(w, req)
		return w
	}
	changedConfirmation := confirmedRequest("another-map")
	if changedConfirmation.Code != http.StatusConflict || !strings.Contains(changedConfirmation.Body.String(), "COMPETENCY_MAP_CHANGED") {
		t.Fatalf("skip with changed confirmation: %d %s", changedConfirmation.Code, changedConfirmation.Body.String())
	}
	check("POST", skipContract, changedConfirmation)
	skipped := confirmedRequest(importedSnapshot.Map.Fingerprint)
	if skipped.Code != http.StatusAccepted {
		t.Fatalf("skip published map: %d %s", skipped.Code, skipped.Body.String())
	}
	check("POST", skipContract, skipped)
	const retryContract = "/admin/subjects/{subject_id}/graph/competency-map/retry"
	retryRequest := func(fingerprint string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://api.example"+examTestPath(mapPath+"/retry"), nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		req.Header.Set("X-Expected-Map-Fingerprint", fingerprint)
		w := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		cookie      *http.Cookie
		fingerprint string
		status      int
	}{
		{nil, importedSnapshot.Map.Fingerprint, http.StatusUnauthorized},
		{access, importedSnapshot.Map.Fingerprint, http.StatusForbidden},
		{admin, importedSnapshot.Map.Fingerprint, http.StatusConflict},
	} {
		response := retryRequest(tc.fingerprint, tc.cookie)
		if response.Code != tc.status {
			t.Fatalf("retry map: %d %s", response.Code, response.Body.String())
		}
		check("POST", retryContract, response)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE course_graph_jobs SET status='failed',attempts=3 WHERE subject_id=$1`, contractSubject.ID); err != nil {
		t.Fatal(err)
	}
	for _, fingerprint := range []string{"", "changed-map"} {
		response := retryRequest(fingerprint, admin)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "COMPETENCY_MAP_CHANGED") {
			t.Fatalf("retry changed map: %d %s", response.Code, response.Body.String())
		}
		check("POST", retryContract, response)
	}
	retried := retryRequest(importedSnapshot.Map.Fingerprint, admin)
	if retried.Code != http.StatusAccepted || !strings.Contains(retried.Body.String(), `"status":"pending"`) || !strings.Contains(retried.Body.String(), `"attempts":0`) {
		t.Fatalf("retry saved map: %d %s", retried.Code, retried.Body.String())
	}
	check("POST", retryContract, retried)
	repeated := retryRequest(importedSnapshot.Map.Fingerprint, admin)
	if repeated.Code != http.StatusConflict {
		t.Fatalf("duplicate retry: %d %s", repeated.Code, repeated.Body.String())
	}
	check("POST", retryContract, repeated)

	check("GET", "/subjects", f.request("GET", "/subjects", "", admin))
	materialData := []byte("%PDF-1.7 lecture")
	materialsUpload := uploadMaterial(f, "/admin/subjects/subject:test/materials", "lecture.pdf", materialData, admin)
	check("POST", "/admin/subjects/{subject_id}/materials", materialsUpload)
	check("GET", "/admin/material-kinds", f.request("GET", "/admin/material-kinds", "", admin))
	check("GET", "/admin/subjects/{subject_id}/materials", f.request("GET", "/admin/subjects/subject:test/materials", "", admin))
	var uploadResult struct {
		Uploaded []struct {
			ID       string `json:"id"`
			S3RawKey string `json:"s3_raw_key"`
		} `json:"uploaded"`
		Failed []struct {
			Filename string `json:"filename"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(materialsUpload.Body.Bytes(), &uploadResult); err != nil || len(uploadResult.Uploaded) == 0 {
		t.Fatalf("materials upload: %s (%v)", materialsUpload.Body.String(), err)
	}
	matID := uploadResult.Uploaded[0].ID
	check("PATCH", "/admin/subjects/{subject_id}/materials/{material_id}", f.request("PATCH", "/admin/subjects/subject:test/materials/"+matID, `{ "week": 3, "kind": "lecture" }`, admin))
	check("POST", "/admin/subjects/{subject_id}/graph/build", f.request("POST", "/admin/subjects/subject:test/graph/build", "", admin))
	// Finish the material's processing as the OCR and cleaning workers would.
	for _, q := range []string{
		`UPDATE subject_materials SET status = 'parsed' WHERE id = $1`,
		`INSERT INTO material_cleaning_jobs(material_id, status, s3_report_key, updated_at) VALUES ($1, 'ready', 'report', 1)
		 ON CONFLICT (material_id) DO UPDATE SET status = 'ready', s3_report_key = 'report'`,
	} {
		if _, err := f.app.pool.Exec(t.Context(), q, matID); err != nil {
			t.Fatal(err)
		}
	}
	check("POST", "/admin/subjects/{subject_id}/graph/build", f.request("POST", "/admin/subjects/subject:test/graph/build", "", admin))
	check("POST", "/admin/subjects/{subject_id}/graph/build", f.request("POST", "/admin/subjects/subject:test/graph/build", "", admin))
	check("GET", "/admin/subjects/{subject_id}/graph", f.request("GET", "/admin/subjects/subject:test/graph", "", admin))
	check("GET", "/admin/subjects/{subject_id}/materials/{material_id}/raw", f.request("GET", "/admin/subjects/subject:test/materials/"+matID+"/raw", "", admin))

	// Publish the source citation with the graph so the diagnostic captures it
	// in its own snapshot before the original file is replaced.
	graphData, err := os.ReadFile(geekFixture)
	if err != nil {
		t.Fatal(err)
	}
	var graph exam.Graph
	if err := json.Unmarshal(graphData, &graph); err != nil {
		t.Fatal(err)
	}
	revisionHash := sha256.Sum256([]byte(uploadResult.Uploaded[0].S3RawKey))
	revision := hex.EncodeToString(revisionHash[:])
	graph.Nodes[0].Sources = []exam.SourceCitation{{MaterialID: matID, MaterialRevision: &revision}}
	graphData, err = json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticID := geekFlow(t, f, access, graphData, check)
	sourcePath := "/diagnostic-sessions/" + diagnosticID + "/materials/" + matID + "/raw"
	sourceContract := "/diagnostic-sessions/{id}/materials/{material_id}/raw"
	source := f.request("GET", sourcePath, "", access)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), materialData) {
		t.Fatalf("diagnostic source: %d %s", source.Code, source.Body.String())
	}
	check("GET", sourceContract, source)
	foreignSource := f.request("GET", sourcePath, "", admin)
	if foreignSource.Code != http.StatusNotFound {
		t.Fatalf("foreign diagnostic source: %d %s", foreignSource.Code, foreignSource.Body.String())
	}
	check("GET", sourceContract, foreignSource)

	var replacementBody bytes.Buffer
	replacementWriter := multipart.NewWriter(&replacementBody)
	replacementPart, err := replacementWriter.CreateFormFile("file", "replacement.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = replacementPart.Write([]byte("%PDF-1.7 replacement"))
	_ = replacementWriter.Close()
	replacementRequest := httptest.NewRequest("PUT", "https://api.example/exam/admin/subjects/subject:test/materials/"+matID+"/raw", &replacementBody)
	replacementRequest.Header.Set("Content-Type", replacementWriter.FormDataContentType())
	replacementRequest.AddCookie(admin)
	replacementResponse := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(replacementResponse, replacementRequest)
	check("PUT", "/admin/subjects/{subject_id}/materials/{material_id}/raw", replacementResponse)
	replacedSource := f.request("GET", sourcePath, "", access)
	if replacedSource.Code != http.StatusGone {
		t.Fatalf("replaced diagnostic source: %d %s", replacedSource.Code, replacedSource.Body.String())
	}
	check("GET", sourceContract, replacedSource)
	check("DELETE", "/admin/subjects/{subject_id}/materials/{material_id}", f.request("DELETE", "/admin/subjects/subject:test/materials/"+matID, "", admin))
	deletedSource := f.request("GET", sourcePath, "", access)
	if deletedSource.Code != http.StatusGone {
		t.Fatalf("deleted diagnostic source: %d %s", deletedSource.Code, deletedSource.Body.String())
	}
	check("GET", sourceContract, deletedSource)

	// Every learning operation is protected by the same real auth middleware.
	// Hit each generated route without credentials so a misspelled/unregistered
	// path fails as 404 instead of being mistaken for an implemented operation.
	parameters := regexp.MustCompile(`\{[^}]+\}`)
	for path, node := range paths {
		if !strings.HasPrefix(path, "/learn/") {
			continue
		}
		for method := range node.(map[string]any) {
			if !strings.Contains(" get post put patch delete head options ", " "+method+" ") {
				continue
			}
			requestPath := parameters.ReplaceAllString(path, "untrusted-id")
			request := httptest.NewRequest(strings.ToUpper(method), "https://api.example"+requestPath, strings.NewReader("{}"))
			response := httptest.NewRecorder()
			f.app.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("learning route must be registered and require auth: %s %s = %d %s", method, path, response.Code, response.Body.String())
				continue
			}
			check(strings.ToUpper(method), path, response)
			checked[path] = true
		}
	}
	outline := f.request("GET", "/learn/subjects/subject:test/diagnostic-outline", "", access)
	if outline.Code != http.StatusOK {
		t.Fatalf("outline: %d %s", outline.Code, outline.Body.String())
	}
	check("GET", "/learn/subjects/{subject_id}/diagnostic-outline", outline)
	check("POST", "/auth/refresh", f.request("POST", "/auth/refresh", "", refresh))
	check("POST", "/auth/logout", f.request("POST", "/auth/logout", "", refresh))
	check("GET", "/health", f.request("GET", "/health", ""))

	for path, node := range paths {
		implemented := false
		for _, value := range node.(map[string]any) {
			if operation, ok := value.(map[string]any); ok && operation["x-implementation"] == "implemented" {
				implemented = true
			}
		}
		if implemented && !checked[path] {
			t.Errorf("no verified handler response for %s", path)
		}
	}
}

func TestHTTPSCookieLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	server := httptest.NewTLSServer(f.handler)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	send := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	response := send("POST", "/auth/register", `{"email":"https@example.edu","password":"`+password+`","display_name":"HTTPS"}`)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 201 || bytes.Contains(body, []byte("access_token")) {
		t.Fatalf("registration: %s", body)
	}
	for _, step := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/auth/me", 200}, {"POST", "/auth/refresh", 200}, {"GET", "/auth/me", 200},
		{"POST", "/auth/logout", 204}, {"GET", "/auth/me", 401},
	} {
		resp := send(step.method, step.path, "")
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != step.status {
			t.Fatalf("cookie lifecycle %s: %d %s", step.path, resp.StatusCode, data)
		}
	}
}
