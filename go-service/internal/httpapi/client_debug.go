package httpapi

import (
	"net/http"
	"strings"
)

// clientDebugHeader carries the plugin's debug mode on every backend request
// ("1" on, "0" off). Requests without it are treated as debug, so callers
// that do not send it keep the full diagnostic responses.
const clientDebugHeader = "X-Archive-Center-Debug"

func clientDebugRequested(r *http.Request) bool {
	value := strings.TrimSpace(r.Header.Get(clientDebugHeader))
	return value != "0"
}
