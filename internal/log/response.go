package log

import (
	"fmt"
)

// LogError logs an error in human-readable format
func LogError(context string, err error) {
	if !enabled {
		return
	}
	logger.Error(fmt.Sprintf("!!! ERROR [%s] %v", context, err))
}
