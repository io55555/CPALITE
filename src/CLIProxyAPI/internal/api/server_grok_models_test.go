package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestGrokModelAdapters(t *testing.T) {
	homeModels := grokModelsFromHomeEntries([]homeModelEntry{
		{id: "home-model", displayName: "Home Model", contextLength: 1234},
	})
	if len(homeModels) != 1 || homeModels[0].ID != "home-model" || homeModels[0].ContextLength != 1234 {
		t.Fatalf("home adapter = %#v", homeModels)
	}

	registryModels := grokModelsFromRegistryInfos([]*registry.ModelInfo{
		{ID: "grok-4", DisplayName: "Grok 4", ContextLength: 256000, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}},
	})
	if len(registryModels) != 1 || registryModels[0].ID != "grok-4" || len(registryModels[0].ReasoningLevels) != 1 {
		t.Fatalf("registry adapter = %#v", registryModels)
	}

	response := grokbuild.BuildResponse(registryModels)
	if len(response.Data) != 1 || response.Data[0].APIBackend != "responses" || !response.Data[0].SupportedInAPI {
		t.Fatalf("grok response = %#v", response)
	}
}

func TestHandleGrokModelsUsesRegistryWhenHomeDisabled(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-grok-model-list-registry"
	modelRegistry.RegisterClient(clientID, "xai", []*registry.ModelInfo{
		{ID: "grok-shell-model", DisplayName: "Grok Shell Model", ContextLength: 256000},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := &Server{}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	server.handleGrokModels(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, model := range response.Data {
		if model.ID == "grok-shell-model" {
			return
		}
	}
	t.Fatalf("registered model missing: %s", recorder.Body.String())
}
