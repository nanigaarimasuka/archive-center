package httpapi

import (
	"net/http"
)

// This backend is a fork deployed by a webhook when its repository is pushed.
// The in-app update fetches upstream's releases, which would replace the fork
// with upstream, so check, download and apply refuse. Tests of the update
// itself turn this off.
var inAppUpdateBlockedForFork = true

const inAppUpdateBlockedMessage = "this backend is a fork, not the upstream release, and is deployed by a webhook when its repository is pushed; in-app updates from upstream are not available"

// refuseForkUpdate answers an update request on the fork; true when it did.
func refuseForkUpdate(w http.ResponseWriter) bool {
	if !inAppUpdateBlockedForFork {
		return false
	}
	writeError(w, http.StatusForbidden, "update_unavailable_on_fork", inAppUpdateBlockedMessage)
	return true
}
