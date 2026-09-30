package helper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"myserver/internal/network/netcheck"
)

const (
	netplanMaxFile  = 256 << 10
	netplanMaxFiles = 50
)

// netplanDir is a variable only so that tests can use a temporary
// directory; nothing else ever changes it.
var netplanDir = "/etc/netplan"

func init() {
	// network-netplan-read takes no arguments, so the caller cannot point it
	// anywhere. It returns the regular *.yaml / *.yml files directly inside
	// /etc/netplan with every secret value redacted; raw secrets never
	// leave this process.
	Register("network-netplan-read", func(_ context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		res := netcheck.NetplanResult{Files: []netcheck.NetplanFile{}}
		entries, err := os.ReadDir(netplanDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return json.NewEncoder(stdout).Encode(res)
			}
			return Userf("Netplan dizini okunamadı.")
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			n := e.Name()
			ext := strings.ToLower(filepath.Ext(n))
			if (ext != ".yaml" && ext != ".yml") || strings.HasPrefix(n, ".") || n != filepath.Base(n) {
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > netplanMaxFiles {
			names = names[:netplanMaxFiles]
		}
		for _, n := range names {
			res.Files = append(res.Files, readNetplanFile(n))
		}
		return json.NewEncoder(stdout).Encode(res)
	})
}

func readNetplanFile(name string) netcheck.NetplanFile {
	out := netcheck.NetplanFile{Name: name}
	path := filepath.Join(netplanDir, name)
	li, err := os.Lstat(path)
	if err != nil {
		out.Error = "Dosya okunamadı."
		return out
	}
	if !li.Mode().IsRegular() {
		out.Error = "Normal bir dosya olmadığı için (ör. sembolik bağlantı) gösterilmiyor."
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		out.Error = "Dosya okunamadı."
		return out
	}
	defer f.Close()
	// The opened file must be the very file that was examined above.
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(li, fi) {
		out.Error = "Dosya okuma sırasında değişti."
		return out
	}
	out.Size = fi.Size()
	out.Mode = fi.Mode().Perm().String()
	out.Modified = fi.ModTime().Unix()
	data, err := io.ReadAll(io.LimitReader(f, netplanMaxFile+1))
	if err != nil {
		out.Error = "Dosya okunamadı."
		return out
	}
	if len(data) > netplanMaxFile {
		data = data[:netplanMaxFile]
		// Drop the possibly cut last line so a secret is never half shown.
		if i := strings.LastIndexByte(string(data), '\n'); i >= 0 {
			data = data[:i]
		}
		out.Truncated = true
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		out.Error = "Dosya metin biçiminde değil."
		return out
	}
	out.Content, out.Redacted = netcheck.RedactNetplan(string(data))
	return out
}
