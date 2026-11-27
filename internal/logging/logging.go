// Package logging configures the loggers used by registry-proxy.
package logging

import (
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

const textTimestampFormat = "2006-01-02 15:04:05"

func init() {
	// Keep logs emitted before registry.NewRegistry (for example the listener
	// banner) in the same format as everything that follows.
	logrus.SetFormatter(TextFormatter())
}

// NewAppLogger builds the logger used by registry-proxy's own components.
//
// It is intentionally independent from distribution's REGISTRY_LOG_LEVEL: the
// internal registry stays at error level, while normal pull events remain
// visible. An empty or invalid level falls back to info.
func NewAppLogger(level string) *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(TextFormatter())
	l.SetLevel(ParseLevel(level))
	return l
}

// TextFormatter is the text format shared by application and registry logs.
func TextFormatter() logrus.Formatter {
	return &logrus.TextFormatter{
		TimestampFormat: textTimestampFormat,
		FullTimestamp:   true,
		DisableColors:   true,
	}
}

// ParseLevel parses a log level, defaulting to info for empty or invalid
// values so a typo never silences the application entirely.
func ParseLevel(level string) logrus.Level {
	parsed, err := logrus.ParseLevel(strings.TrimSpace(level))
	if err != nil {
		return logrus.InfoLevel
	}
	return parsed
}

// PrepareRegistryLogger aligns distribution's global logger with the
// application format and suppresses expected misses produced by regclient.
//
// During a copy regclient probes the local registry with HEAD requests for
// manifests and blobs; a 404 is the normal "not present yet" answer and would
// otherwise log several error lines per layer. Real client-facing 404s keep
// their log entries because their user agent is not regclient.
//
// Call it after registry.NewRegistry, which reconfigures the global logrus
// logger from the distribution configuration.
func PrepareRegistryLogger() {
	std := logrus.StandardLogger()
	base := std.Formatter
	if f, ok := base.(*filteredFormatter); ok {
		base = f.base
	}
	if _, ok := base.(*logrus.TextFormatter); ok {
		base = TextFormatter()
	}
	std.SetFormatter(&filteredFormatter{base: base})
}

type filteredFormatter struct {
	base logrus.Formatter
}

func (f *filteredFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	if isExpectedMiss(entry) {
		return nil, nil
	}
	return f.base.Format(entry)
}

func isExpectedMiss(entry *logrus.Entry) bool {
	if entry.Level != logrus.ErrorLevel || entry.Message != "response completed with error" {
		return false
	}
	status, ok := entry.Data["http.response.status"].(int)
	if !ok || status != http.StatusNotFound {
		return false
	}
	ua, ok := entry.Data["http.request.useragent"].(string)
	return ok && strings.HasPrefix(ua, "regclient/")
}
