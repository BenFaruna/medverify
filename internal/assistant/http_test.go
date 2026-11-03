package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/platform/auth"
)

func testRouter(handler *HTTPHandler) http.Handler {
	router := chi.NewRouter()
	router.Use(auth.APIKey("test-key"))
	router.Post("/v1/scans/{id}/questions", handler.Ask)
	router.Get("/v1/scans/{id}/conversation", handler.Conversation)
	return router
}

func TestAskEndpointRequiresAuthentication(t *testing.T) {
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, llm.NewFake(), &fakeTranscriptStore{}, scans, &fakeSearcher{})
	request := httptest.NewRequest(http.MethodPost, "/v1/scans/"+scans.scanned.ID.String()+"/questions",
		strings.NewReader(`{"question":"hello"}`))
	response := httptest.NewRecorder()
	testRouter(NewHTTPHandler(HTTPOptions{Service: service})).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestAskEndpointRejectsMalformedBodies(t *testing.T) {
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, llm.NewFake(), &fakeTranscriptStore{}, scans, &fakeSearcher{})
	handler := testRouter(NewHTTPHandler(HTTPOptions{Service: service}))
	for _, body := range []string{`{}`, `{"question":""}`, `not json`, `{"question":"hi","extra":true}`} {
		request := httptest.NewRequest(http.MethodPost, "/v1/scans/"+scans.scanned.ID.String()+"/questions", strings.NewReader(body))
		request.Header.Set(auth.HeaderAPIKey, "test-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("body %q status = %d", body, response.Code)
		}
	}
}

func TestAskEndpointReturnsAForbiddenAnswer(t *testing.T) {
	scans := &fakeScans{scanned: scannedFixture()}
	scans.scanned.ClientKey = auth.DefaultClientKey
	client := llm.NewFake(routeResponse("personal_safety"))
	service := newTestService(t, client, &fakeTranscriptStore{}, scans, &fakeSearcher{})
	handler := testRouter(NewHTTPHandler(HTTPOptions{Service: service}))

	request := httptest.NewRequest(http.MethodPost, "/v1/scans/"+scans.scanned.ID.String()+"/questions",
		strings.NewReader(`{"question":"Is this okay for my child?"}`))
	request.Header.Set(auth.HeaderAPIKey, "test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body AnswerView
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Verified || !strings.Contains(body.Answer, "pharmacist") {
		t.Fatalf("body = %#v", body)
	}
	if strings.Contains(response.Body.String(), "question") {
		t.Fatal("the response must not echo the user question")
	}
}

func TestAskEndpointReturnsRetryableProviderFailure(t *testing.T) {
	scans := &fakeScans{scanned: scannedFixture()}
	scans.scanned.ClientKey = auth.DefaultClientKey
	service := newTestService(t, llm.NewFake(llm.FakeResponse{Err: context.DeadlineExceeded}), &fakeTranscriptStore{}, scans, &fakeSearcher{})
	handler := testRouter(NewHTTPHandler(HTTPOptions{Service: service}))
	request := httptest.NewRequest(http.MethodPost, "/v1/scans/"+scans.scanned.ID.String()+"/questions",
		strings.NewReader(`{"question":"anything"}`))
	request.Header.Set(auth.HeaderAPIKey, "test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("a retryable error must tell the client when to retry")
	}
}

func TestConversationEndpointReturnsTranscript(t *testing.T) {
	scans := &fakeScans{scanned: scannedFixture()}
	scans.scanned.ClientKey = auth.DefaultClientKey
	store := &fakeTranscriptStore{exists: true, conversation: Conversation{
		ID: uuid.New(), ScanID: scans.scanned.ID, Language: "en",
		ExpiresAt: time.Now().Add(time.Hour),
	}, messages: []Message{{Turn: 1, Role: "user", Content: "q"}}}
	service := newTestService(t, llm.NewFake(), store, scans, &fakeSearcher{})
	handler := testRouter(NewHTTPHandler(HTTPOptions{Service: service}))
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/"+scans.scanned.ID.String()+"/conversation", nil)
	request.Header.Set(auth.HeaderAPIKey, "test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}
