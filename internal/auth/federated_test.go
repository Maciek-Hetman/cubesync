package auth

import (
	"encoding/json"
	"testing"
)

func TestParseBooleanClaim(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		raw  string
		want bool
	}{
		"boolean true":  {raw: `true`, want: true},
		"string true":   {raw: `"true"`, want: true},
		"boolean false": {raw: `false`, want: false},
		"invalid":       {raw: `"yes"`, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := parseBooleanClaim(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestProviderMetadata(t *testing.T) {
	t.Parallel()
	if _, _, err := providerMetadata("google"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := providerMetadata("apple"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := providerMetadata("unknown"); err == nil {
		t.Fatal("expected unsupported provider to fail")
	}
}
