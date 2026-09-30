// Command myserver-helper is the root helper invoked by the panel through
// sudo. See package helper.
package main

import (
	"os"

	"myserver/internal/helper"
)

func main() { os.Exit(helper.Main(os.Args)) }
