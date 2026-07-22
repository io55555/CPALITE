package packetcapture

import "testing"

func TestRuleMatchesMetaXAIProviderSourceAliases(t *testing.T) {
	apiKeyMeta := Record{Provider: "xai", Source: "xai-api-key"}
	authFileMeta := Record{Provider: "xai", Source: "xai-auth-file"}

	if !ruleMatchesMeta(Rule{Provider: "xai"}, apiKeyMeta) {
		t.Fatal("provider xai should match xai api-key source")
	}
	if !ruleMatchesMeta(Rule{Provider: "xai"}, authFileMeta) {
		t.Fatal("provider xai should match xai auth-file source")
	}
	if !ruleMatchesMeta(Rule{Provider: "xai-api-key"}, apiKeyMeta) {
		t.Fatal("provider xai-api-key should match api-key source")
	}
	if ruleMatchesMeta(Rule{Provider: "xai-api-key"}, authFileMeta) {
		t.Fatal("provider xai-api-key should not match auth-file source")
	}
	if !ruleMatchesMeta(Rule{Provider: "xai-auth-file"}, authFileMeta) {
		t.Fatal("provider xai-auth-file should match auth-file source")
	}
	if ruleMatchesMeta(Rule{Provider: "xai-auth-file"}, apiKeyMeta) {
		t.Fatal("provider xai-auth-file should not match api-key source")
	}
}
