package proxy

import (
	"testing"

	"quickwork-openai/internal/config"
)

func TestMapModelAcceptsCatalogPrefixes(t *testing.T) {
	t.Parallel()

	server := &Server{cfg: &config.Config{DefaultModel: "smart"}}
	cases := []struct {
		model string
		want  string
	}{
		{model: "smart", want: "smart"},
		{model: "quickwork/fast", want: "fast"},
		{model: "aws-quick/smart", want: "smart"},
		{model: "aws-quick/balanced", want: "balanced"},
		{model: "aws-quick/auto", want: "auto"},
		{model: "unknown", want: "smart"},
	}
	for _, tc := range cases {
		if got := server.mapModel(tc.model); got != tc.want {
			t.Errorf("mapModel(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}
