package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const clearValue = "test-clearValue-value"

func TestSecretIsMaskedEverywhere(t *testing.T) {
	gh := GitHub{Token: clearValue}

	outputs := map[string]string{
		"%v":       fmt.Sprintf("%v", gh),
		"%+v":      fmt.Sprintf("%+v", gh),
		"%#v":      fmt.Sprintf("%#v", gh),
		"String()": gh.Token.String(),
		"%q":       fmt.Sprintf("%q", gh.Token),
	}

	js, err := json.Marshal(gh)
	if err != nil {
		t.Fatal(err)
	}
	outputs["json"] = string(js)

	ym, err := yaml.Marshal(gh)
	if err != nil {
		t.Fatal(err)
	}
	outputs["yaml"] = string(ym)

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "token", gh.Token)
	outputs["slog"] = buf.String()

	for name, out := range outputs {
		if strings.Contains(out, clearValue) {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
		if !strings.Contains(out, masked) {
			t.Errorf("%s does not show the mask: %s", name, out)
		}
	}

	if gh.Token.Reveal() != clearValue {
		t.Error("Reveal must return the clearValue value")
	}
}

func TestEmptySecretPrintsEmpty(t *testing.T) {
	var s Secret
	if s.String() != "" || s.IsSet() {
		t.Errorf("empty secret: String()=%q IsSet()=%v", s.String(), s.IsSet())
	}
}
