package executor

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatClientResponseFormatFallsBackFromProviderID(t *testing.T) {
	// blank import path ensures translators registered via init in main binary;
	// in package tests, registry may be empty - still verify SourceFormat fallback
	// when HasResponseTransformer returns false for unknown provider ids.
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai"),
		ResponseFormat: sdktranslator.FromString("m365-custom-provider"),
	}
	got := openaiCompatClientResponseFormat(opts)
	if got != sdktranslator.FromString("openai") {
		t.Fatalf("openaiCompatClientResponseFormat() = %q, want openai", got)
	}
}

func TestOpenAICompatClientResponseFormatKeepsMatchingSource(t *testing.T) {
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai"),
		ResponseFormat: sdktranslator.FromString("openai"),
	}
	got := openaiCompatClientResponseFormat(opts)
	if got != sdktranslator.FromString("openai") {
		t.Fatalf("openaiCompatClientResponseFormat() = %q, want openai", got)
	}
}
