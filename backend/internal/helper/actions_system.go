package helper

import (
	"context"
	"fmt"
	"io"

	"myserver/internal/settings"
)

func init() {
	Register("ping", func(_ context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, "pong")
		return err
	})

	Register("hostname-set", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 1); err != nil {
			return err
		}
		if !settings.ValidHostname(args[0]) {
			return Userf("Sunucu adı geçersiz.")
		}
		return Exec(ctx, stdout, "/usr/bin/hostnamectl", "set-hostname", "--", args[0])
	})

	Register("timezone-set", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 1); err != nil {
			return err
		}
		if !settings.ValidTimezone(args[0]) {
			return Userf("Saat dilimi geçersiz.")
		}
		return Exec(ctx, stdout, "/usr/bin/timedatectl", "set-timezone", "--", args[0])
	})
}
