package logging

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestFilteredFormatterDropsExpectedRegclientMisses(t *testing.T) {
	tests := []struct {
		name      string
		level     logrus.Level
		message   string
		fields    logrus.Fields
		wantEmpty bool
	}{
		{
			name:      "regclient manifest miss",
			level:     logrus.ErrorLevel,
			message:   "response completed with error",
			fields:    logrus.Fields{"http.response.status": 404, "http.request.useragent": "regclient/regclient (v0.0.0)"},
			wantEmpty: true,
		},
		{
			name:      "client miss is kept",
			level:     logrus.ErrorLevel,
			message:   "response completed with error",
			fields:    logrus.Fields{"http.response.status": 404, "http.request.useragent": "containerd/v2.0"},
			wantEmpty: false,
		},
		{
			name:      "regclient server error is kept",
			level:     logrus.ErrorLevel,
			message:   "response completed with error",
			fields:    logrus.Fields{"http.response.status": 500, "http.request.useragent": "regclient/regclient (v0.0.0)"},
			wantEmpty: false,
		},
		{
			name:      "unrelated error is kept",
			level:     logrus.ErrorLevel,
			message:   "storage driver failure",
			fields:    logrus.Fields{"http.response.status": 404},
			wantEmpty: false,
		},
		{
			name:      "info miss is kept",
			level:     logrus.InfoLevel,
			message:   "response completed with error",
			fields:    logrus.Fields{"http.response.status": 404, "http.request.useragent": "regclient/regclient (v0.0.0)"},
			wantEmpty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logrus.New()
			logger.SetOutput(&bytes.Buffer{})
			entry := logrus.NewEntry(logger).WithFields(tt.fields)
			entry.Level = tt.level
			entry.Message = tt.message

			got, err := (&filteredFormatter{base: TextFormatter()}).Format(entry)
			if err != nil {
				t.Fatalf("Format() error = %v", err)
			}
			if tt.wantEmpty && len(got) != 0 {
				t.Fatalf("Format() = %q, want empty output", got)
			}
			if !tt.wantEmpty && len(got) == 0 {
				t.Fatal("Format() = empty, want output")
			}
			if !tt.wantEmpty && !strings.Contains(string(got), tt.message) {
				t.Fatalf("Format() = %q, missing message %q", got, tt.message)
			}
		})
	}
}

func TestTextFormatterUsesStableTimestamp(t *testing.T) {
	logger := logrus.New()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetFormatter(TextFormatter())
	logger.WithField("k", "v").Info("hello")

	line := buf.String()
	if !strings.Contains(line, `msg=hello`) {
		t.Fatalf("log line = %q, want msg=hello", line)
	}
	if !strings.Contains(line, `level=info`) {
		t.Fatalf("log line = %q, want level=info", line)
	}
	// A space-separated timestamp keeps the format compact and identical for
	// application and registry logs.
	if !strings.Contains(line, "time=\"20") || strings.Contains(line, "T") {
		t.Fatalf("log line = %q, want space-separated timestamp", line)
	}
}
