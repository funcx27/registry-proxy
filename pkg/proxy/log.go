package proxy

import (
	"os"

	"registry-proxy/internal/logging"
)

// logger is the application logger. APP_LOG_LEVEL controls its verbosity
// independently from distribution's REGISTRY_LOG_LEVEL, so normal pull events
// can stay visible while the internal registry only logs errors.
var logger = logging.NewAppLogger(os.Getenv("APP_LOG_LEVEL"))
