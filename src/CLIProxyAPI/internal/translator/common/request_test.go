package common

import "testing"

func TestRequestModelName(t *testing.T) {
	tests := []struct {
		name     string
		original []byte
		request  []byte
		want     string
	}{
		{name: "original top level", original: []byte(`{"model":"gpt-5.4"}`), request: []byte(`{"model":"translated"}`), want: "gpt-5.4"},
		{name: "original nested", original: []byte(`{"request":{"model":"gpt-5.5"}}`), want: "gpt-5.5"},
		{name: "fallback request", original: []byte(`{"model":""}`), request: []byte(`{"request":{"model":"gpt-5.6"}}`), want: "gpt-5.6"},
		{name: "invalid", original: []byte(`{`), request: nil, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RequestModelName(test.original, test.request); got != test.want {
				t.Fatalf("RequestModelName() = %q, want %q", got, test.want)
			}
		})
	}
}
