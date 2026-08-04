package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatRequestScopedStatusForInvalidRequestSize(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge} {
		if !openAICompatRequestScopedStatus(status) {
			t.Fatalf("status %d should be openai-compat request scoped", status)
		}
	}
	if (statusErr{code: http.StatusBadRequest}).IsRequestScoped() {
		t.Fatal("bare statusErr 400 should not be globally request scoped")
	}
	if (statusErr{code: http.StatusUnauthorized}).IsRequestScoped() {
		t.Fatal("401 should remain credential scoped")
	}
}

func TestOpenAICompatExecutorBadRequestIsRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}`))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	_, err := exec.Execute(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai"),
		ResponseFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	requestScoped, ok := err.(interface{ IsRequestScoped() bool })
	if !ok || !requestScoped.IsRequestScoped() {
		t.Fatalf("expected request-scoped 400, got %T %v", err, err)
	}
}
