package xai

import "testing"

func TestExtractSSOCookie(t *testing.T) {
	if got := ExtractSSOCookie(nil); got != "" {
		t.Fatalf("nil meta => %q", got)
	}
	if got := ExtractSSOCookie(map[string]any{"sso": "  abc  "}); got != "abc" {
		t.Fatalf("sso => %q", got)
	}
	if got := ExtractSSOCookie(map[string]any{"sso_cookie": "sso=xyz"}); got != "xyz" {
		t.Fatalf("sso_cookie prefix => %q", got)
	}
	if got := ExtractSSOCookie(map[string]any{"ssoCookie": "tok"}); got != "tok" {
		t.Fatalf("ssoCookie => %q", got)
	}
	if got := ExtractSSOCookie(map[string]any{"SSOcookie": "ssoVal"}); got != "ssoVal" {
		t.Fatalf("SSOcookie => %q", got)
	}
	if got := ExtractSSOCookie(map[string]any{"other": "nope"}); got != "" {
		t.Fatalf("missing => %q", got)
	}
}

func TestResolveReviveProxyURL(t *testing.T) {
	if got := ResolveReviveProxyURL("socks5://a:1", "http://b:2"); got != "socks5://a:1" {
		t.Fatalf("auth first => %q", got)
	}
	if got := ResolveReviveProxyURL("direct", "http://b:2"); got != "direct" {
		t.Fatalf("auth direct wins => %q", got)
	}
	if got := ResolveReviveProxyURL("", "http://b:2"); got != "http://b:2" {
		t.Fatalf("cpa fallback => %q", got)
	}
	if got := ResolveReviveProxyURL("", ""); got != "direct" {
		t.Fatalf("final direct => %q", got)
	}
}

func TestApplyTokenDataToMetadataPreservesSSO(t *testing.T) {
	meta := map[string]any{"sso": "keep-me", "email": "old@x.ai"}
	token := &TokenData{AccessToken: "at", RefreshToken: "rt", Email: "new@x.ai", Subject: "sub1", ExpiresIn: 100, Expire: "2030-01-01T00:00:00Z"}
	out := ApplyTokenDataToMetadata(meta, token, "keep-me")
	if out["access_token"] != "at" || out["refresh_token"] != "rt" {
		t.Fatalf("tokens not applied: %#v", out)
	}
	if out["sso"] != "keep-me" {
		t.Fatalf("sso lost: %#v", out["sso"])
	}
	if out["disabled"] != false {
		t.Fatalf("disabled=%v", out["disabled"])
	}
}
