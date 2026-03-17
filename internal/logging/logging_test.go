package logging

import (
	"log/slog"
	"testing"
)

func TestParseLevel_Debug(t *testing.T) {
	if got := parseLevel("debug"); got != slog.LevelDebug {
		t.Errorf("parseLevel(\"debug\") = %v, want %v", got, slog.LevelDebug)
	}
}

func TestParseLevel_Warn(t *testing.T) {
	if got := parseLevel("warn"); got != slog.LevelWarn {
		t.Errorf("parseLevel(\"warn\") = %v, want %v", got, slog.LevelWarn)
	}
}

func TestParseLevel_Error(t *testing.T) {
	if got := parseLevel("error"); got != slog.LevelError {
		t.Errorf("parseLevel(\"error\") = %v, want %v", got, slog.LevelError)
	}
}

func TestParseLevel_Default(t *testing.T) {
	if got := parseLevel(""); got != slog.LevelInfo {
		t.Errorf("parseLevel(\"\") = %v, want %v", got, slog.LevelInfo)
	}
}

func TestParseLevel_CaseInsensitive(t *testing.T) {
	if got := parseLevel("DEBUG"); got != slog.LevelDebug {
		t.Errorf("parseLevel(\"DEBUG\") = %v, want %v", got, slog.LevelDebug)
	}
}

func TestNew_ReturnsLogger(t *testing.T) {
	logger := New("test-component")
	if logger == nil {
		t.Fatal("New returned nil")
	}
}

func TestNew_WithJsonFormat(t *testing.T) {
	t.Setenv("LOG_FORMAT", "json")
	logger := New("json-component")
	if logger == nil {
		t.Fatal("New returned nil with json format")
	}
}
