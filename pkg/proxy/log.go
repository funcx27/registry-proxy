package proxy

import (
	"registry-proxy/internal/logging"

	"github.com/sirupsen/logrus"
)

// logger is the application logger. APP_LOG_LEVEL controls its verbosity
// independently from distribution's REGISTRY_LOG_LEVEL, so normal pull events
// can stay visible while the internal registry only logs errors.
var logger = logging.NewAppLogger("info")

// SetLogger replaces the application logger. The process calls it once during
// startup, before serving requests.
func SetLogger(l *logrus.Logger) {
	if l != nil {
		logger = l
	}
}
