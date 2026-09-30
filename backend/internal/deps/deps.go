// Package deps pins third-party dependencies used by feature modules so that
// go.mod stays stable while modules are developed independently.
package deps

import (
	_ "github.com/creack/pty"
	_ "github.com/docker/docker/client"
	_ "github.com/gorilla/websocket"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/sys/unix"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
