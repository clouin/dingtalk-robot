package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

// textLine 匹配期望的文本行：秒级 RFC3339 时间、小写级别、加引号的消息。
var textLine = regexp.MustCompile(
	`^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(Z|[+-]\d{2}:\d{2}) level=info msg="configuration loaded" version=dev\n$`)

func TestTextFormatShape(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, "text")

	logger.Info("configuration loaded", "version", "dev")

	got := buf.String()
	if !textLine.MatchString(got) {
		t.Errorf("text line %q does not match the watchtower-style shape %s", got, textLine)
	}
	if strings.Contains(got, "level=INFO") {
		t.Error("level must be lower case")
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("expected exactly one line, got %q", got)
	}
}

func TestJSONFormatIsParseable(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, "json")

	logger.Info("listening", "addr", ":8080")

	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json output %q is not parseable: %v", buf.String(), err)
	}
	for _, key := range []string{"time", "level", "msg"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("json output missing %q: %v", key, parsed)
		}
	}
	if parsed["level"] != "info" {
		t.Errorf("level = %v, want lower-case info", parsed["level"])
	}
	raw, _ := parsed["time"].(string)
	if _, err := time.Parse(time.RFC3339, raw); err != nil {
		t.Errorf("time %q is not second-precision RFC3339: %v", raw, err)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelWarn, "text")

	logger.Info("should be dropped")
	logger.Debug("should be dropped")

	if buf.Len() != 0 {
		t.Errorf("warn level still emitted info/debug records: %q", buf.String())
	}

	logger.Warn("kept")

	if !strings.Contains(buf.String(), "level=warn") || !strings.Contains(buf.String(), "msg=kept") {
		t.Errorf("warn record missing or malformed: %q", buf.String())
	}
}
