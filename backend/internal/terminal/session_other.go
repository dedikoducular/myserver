//go:build !linux

package terminal

import (
	"net/http"

	"myserver/internal/httpx"
)

// handleSocket: pseudo-terminals are only implemented for Linux, the
// platform the panel is deployed on.
func (m *Module) handleSocket(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, r, httpx.Unavailable("terminal_unsupported", "Terminal bu işletim sisteminde desteklenmiyor."))
}
