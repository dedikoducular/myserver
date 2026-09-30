package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"myserver/internal/security/fwcheck"
)

const (
	ufwOutputCap  = 1 << 20
	ufwDefaultCfg = "/etc/default/ufw"
	ufwMainCfg    = "/etc/ufw/ufw.conf"
)

func ufwInstalled() bool {
	fi, err := os.Stat(fwcheck.UfwPath)
	return err == nil && fi.Mode().IsRegular()
}

// ufwRead runs a read-only ufw command and returns its output.
func ufwRead(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := Command(ctx, fwcheck.UfwPath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	b := out.Bytes()
	if len(b) > ufwOutputCap {
		b = b[:ufwOutputCap]
	}
	return string(b), nil
}

// ufwChange runs a state-changing ufw command. ufw's own messages go to the
// helper's stderr (they end up in the panel log, not in front of the user).
func ufwChange(ctx context.Context, stdin io.Reader, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := Command(ctx, fwcheck.UfwPath, args...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func readSmall(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return ""
	}
	return string(b)
}

func init() {
	// firewall-status: no arguments; returns ufw's status texts as JSON.
	Register("firewall-status", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		var st fwcheck.RawStatus
		if !ufwInstalled() {
			return json.NewEncoder(stdout).Encode(st)
		}
		st.Installed = true
		var err error
		if st.Verbose, err = ufwRead(ctx, "status", "verbose"); err != nil {
			return Userf("Güvenlik duvarı durumu okunamadı.")
		}
		if st.Numbered, err = ufwRead(ctx, "status", "numbered"); err != nil {
			return Userf("Güvenlik duvarı kuralları okunamadı.")
		}
		if st.Added, err = ufwRead(ctx, "show", "added"); err != nil {
			return Userf("Güvenlik duvarı kuralları okunamadı.")
		}
		st.DefaultConf = readSmall(ufwDefaultCfg)
		st.UfwConf = readSmall(ufwMainCfg)
		return json.NewEncoder(stdout).Encode(st)
	})

	// firewall-enable: no arguments. --force only suppresses ufw's
	// interactive "may disrupt existing ssh connections" question; the
	// panel has already checked the access rules and asked the user.
	Register("firewall-enable", func(ctx context.Context, args []string, _ io.Reader, _ io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		if !ufwInstalled() {
			return Userf("UFW kurulu değil.")
		}
		if err := ufwChange(ctx, nil, "--force", "enable"); err != nil {
			return Userf("Güvenlik duvarı etkinleştirilemedi.")
		}
		return nil
	})

	// firewall-disable: no arguments.
	Register("firewall-disable", func(ctx context.Context, args []string, _ io.Reader, _ io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		if !ufwInstalled() {
			return Userf("UFW kurulu değil.")
		}
		if err := ufwChange(ctx, nil, "disable"); err != nil {
			return Userf("Güvenlik duvarı devre dışı bırakılamadı.")
		}
		return nil
	})

	// firewall-rule-add <action> <port> <protocol> <source> <comment>
	// comment may be empty.
	Register("firewall-rule-add", func(ctx context.Context, args []string, _ io.Reader, _ io.Writer) error {
		if err := ArgCount(args, 5); err != nil {
			return err
		}
		spec, err := fwcheck.RuleSpec{
			Action: args[0], Port: args[1], Protocol: args[2], Source: args[3], Comment: args[4],
		}.Normalize()
		if err != nil {
			return Userf("%s", err.Error())
		}
		if !ufwInstalled() {
			return Userf("UFW kurulu değil.")
		}
		if err := ufwChange(ctx, nil, spec.Args()...); err != nil {
			return Userf("Kural eklenemedi.")
		}
		return nil
	})

	// firewall-rule-delete <origin> <number> <id>
	// origin "numbered": the rule at <number> in `ufw status numbered` must
	// still have the text <id>; it is then deleted by number.
	// origin "added": line <number> of `ufw show added` must equal <id>; the
	// rule is then deleted by its specification.
	Register("firewall-rule-delete", func(ctx context.Context, args []string, _ io.Reader, _ io.Writer) error {
		if err := ArgCount(args, 3); err != nil {
			return err
		}
		origin, id := args[0], args[2]
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > 100000 || strconv.Itoa(n) != args[1] {
			return Userf("Kural numarası geçersiz.")
		}
		if id == "" || len(id) > 400 || strings.ContainsAny(id, "\n\r\x00") {
			return Userf("Kural tanımı geçersiz.")
		}
		if origin != fwcheck.OriginNumbered && origin != fwcheck.OriginAdded {
			return Userf("Kural kaynağı geçersiz.")
		}
		if !ufwInstalled() {
			return Userf("UFW kurulu değil.")
		}
		const changed = "Kural listesi değişmiş; kural silinmedi. Listeyi yenileyip tekrar deneyin."
		switch origin {
		case fwcheck.OriginNumbered:
			text, err := ufwRead(ctx, "status", "numbered")
			if err != nil {
				return Userf("Güvenlik duvarı kuralları okunamadı.")
			}
			if !ruleMatches(fwcheck.ParseNumbered(text), n, id) {
				return Userf(changed)
			}
			// `ufw delete N` asks for confirmation on stdin; answer it
			// instead of using --force.
			if err := ufwChange(ctx, strings.NewReader("y\n"), "delete", strconv.Itoa(n)); err != nil {
				return Userf("Kural silinemedi.")
			}
			return nil
		case fwcheck.OriginAdded:
			text, err := ufwRead(ctx, "show", "added")
			if err != nil {
				return Userf("Güvenlik duvarı kuralları okunamadı.")
			}
			if !ruleMatches(fwcheck.ParseAdded(text), n, id) {
				return Userf(changed)
			}
			del, ok := fwcheck.DeleteArgs(id)
			if !ok {
				return Userf("Bu kural panelden silinemiyor; sunucuda ufw komutuyla silinmelidir.")
			}
			if err := ufwChange(ctx, nil, del...); err != nil {
				return Userf("Kural silinemedi.")
			}
			return nil
		default:
			return Userf("Kural kaynağı geçersiz.")
		}
	})
}

func ruleMatches(rules []fwcheck.Rule, number int, id string) bool {
	for _, r := range rules {
		if r.Number == number {
			return r.ID == id
		}
	}
	return false
}
