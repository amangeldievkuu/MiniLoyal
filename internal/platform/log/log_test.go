package log

import (
	"bytes"
	"strings"
	"testing"
)

func TestSetupJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := Setup(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden")
	logger.Info("shown", "k", "v")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, `"k":"v"`) {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	if _, err := Setup(&bytes.Buffer{}, "loud", "json"); err == nil {
		t.Error("expected level error")
	}
	if _, err := Setup(&bytes.Buffer{}, "info", "xml"); err == nil {
		t.Error("expected format error")
	}
}
