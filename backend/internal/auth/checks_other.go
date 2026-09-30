//go:build !linux

package auth

import "context"

// MyServer only runs on Linux; these exist so the package compiles for
// unit tests on a development machine.

func checkStorage(string) Check {
	return Check{OK: false, Message: "Bu işletim sistemi desteklenmiyor."}
}

func checkDocker(context.Context, string) Check {
	return Check{OK: false, Message: "Bu işletim sistemi desteklenmiyor."}
}
