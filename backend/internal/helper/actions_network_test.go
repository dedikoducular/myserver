package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"myserver/internal/network/netcheck"
)

const netSecret = "cok-gizli-parola-123"

// netUseDir points the action at a temporary directory instead of /etc/netplan.
func netUseDir(t *testing.T, dir string) {
	t.Helper()
	old := netplanDir
	netplanDir = dir
	t.Cleanup(func() { netplanDir = old })
}

func netRead(t *testing.T, args ...string) (netcheck.NetplanResult, string, error) {
	t.Helper()
	var out bytes.Buffer
	err := actions["network-netplan-read"](context.Background(), args, strings.NewReader(""), &out)
	var res netcheck.NetplanResult
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
			t.Fatalf("output is not JSON: %v: %q", jerr, out.String())
		}
	}
	return res, out.String(), err
}

func netWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func netFile(t *testing.T, res netcheck.NetplanResult, name string) netcheck.NetplanFile {
	t.Helper()
	for _, f := range res.Files {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("%s missing from %+v", name, res.Files)
	return netcheck.NetplanFile{}
}

func TestNetplanReadTakesNoArguments(t *testing.T) {
	dir := t.TempDir()
	netUseDir(t, dir)
	netWrite(t, dir, "01.yaml", "network: {version: 2}\n")
	for _, args := range [][]string{{"/etc/shadow"}, {"../shadow"}, {""}, {"--dir", "/etc"}} {
		_, out, err := netRead(t, args...)
		if err == nil || out != "" {
			t.Errorf("%q: accepted (%q)", args, out)
		}
	}
}

func TestNetplanReadDefaultDirectory(t *testing.T) {
	if netplanDir != "/etc/netplan" {
		t.Errorf("the action reads %q", netplanDir)
	}
}

func TestNetplanReadMissingDirectory(t *testing.T) {
	netUseDir(t, filepath.Join(t.TempDir(), "absent"))
	res, out, err := netRead(t)
	if err != nil || res.Files == nil || len(res.Files) != 0 || !strings.Contains(out, `"files":[]`) {
		t.Errorf("got %+v %q %v", res, out, err)
	}
}

func TestNetplanReadRedactsAndSelects(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("needs symbolic links and Unix permissions")
	}
	dir := t.TempDir()
	netUseDir(t, dir)
	outside := t.TempDir()
	netWrite(t, outside, "secret.yaml", "password: "+netSecret+"\n")
	netWrite(t, outside, "plain.txt", "outside-content\n")

	netWrite(t, dir, "50-cloud-init.yaml", "network:\n  version: 2\n  wifis:\n    wlan0:\n      access-points:\n        \"Ev Agi\": {password: "+netSecret+"}\n        misafir:\n          password: \""+netSecret+"\"\n")
	netWrite(t, dir, "60-wg.yml", "network:\n  tunnels:\n    wg0:\n      mode: wireguard\n      key: 4GgaQCy68nzNsUE5aJ9fuLzHhB65tAlwbmA72MWnOm8=\n")
	netWrite(t, dir, "UPPER.YAML", "network: {version: 2}\n")
	netWrite(t, dir, "notes.txt", "password: "+netSecret+"\n")
	netWrite(t, dir, "backup.yaml.bak", "password: "+netSecret+"\n")
	netWrite(t, dir, ".hidden.yaml", "password: "+netSecret+"\n")
	netWrite(t, dir, "binary.yaml", "password: "+netSecret+"\x00\xff\xfe\n")
	netWrite(t, dir, "latin1.yaml", "password: "+netSecret+"\n# \xe7\xf6\n")
	netWrite(t, dir, "empty.yaml", "")
	if err := os.Symlink(filepath.Join(outside, "secret.yaml"), filepath.Join(dir, "70-link.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "plain.txt"), filepath.Join(dir, "71-link.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "80-dir.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	netWrite(t, filepath.Join(dir, "80-dir.yaml"), "inner.yaml", "password: "+netSecret+"\n")

	res, out, err := netRead(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, netSecret) || strings.Contains(out, "4GgaQCy68nz") || strings.Contains(out, "outside-content") {
		t.Fatalf("a secret or an outside file left the helper:\n%s", out)
	}
	names := []string{}
	for _, f := range res.Files {
		names = append(names, f.Name)
	}
	want := "50-cloud-init.yaml 60-wg.yml 70-link.yaml 71-link.yaml 80-dir.yaml UPPER.YAML binary.yaml empty.yaml latin1.yaml"
	if strings.Join(names, " ") != want {
		t.Errorf("files\n got %s\nwant %s", strings.Join(names, " "), want)
	}
	f := netFile(t, res, "50-cloud-init.yaml")
	if f.Error != "" || f.Redacted != 2 || !strings.Contains(f.Content, "Ev Agi") || !strings.Contains(f.Content, netcheck.RedactionMask) ||
		f.Size == 0 || f.Mode != "-rw-------" || f.Modified == 0 || f.Truncated {
		t.Errorf("50-cloud-init.yaml: %+v", f)
	}
	if f := netFile(t, res, "60-wg.yml"); f.Redacted != 1 || !strings.Contains(f.Content, "mode: wireguard") {
		t.Errorf("60-wg.yml: %+v", f)
	}
	for _, n := range []string{"70-link.yaml", "71-link.yaml", "80-dir.yaml", "binary.yaml", "latin1.yaml"} {
		if f := netFile(t, res, n); f.Error == "" || f.Content != "" {
			t.Errorf("%s must be refused: %+v", n, f)
		}
	}
	if f := netFile(t, res, "empty.yaml"); f.Error != "" || f.Content != "" || f.Redacted != 0 {
		t.Errorf("empty.yaml: %+v", f)
	}
}

func TestNetplanReadTruncatesOnLineBoundary(t *testing.T) {
	dir := t.TempDir()
	netUseDir(t, dir)
	var b strings.Builder
	line := "      # " + strings.Repeat("x", 90) + "\n"
	for b.Len() < netplanMaxFile-len(line) {
		b.WriteString(line)
	}
	// The secret line straddles the size limit.
	pad := netplanMaxFile - b.Len() - len("password: ") - 5
	b.WriteString(strings.Repeat(" ", pad))
	b.WriteString("password: " + netSecret + "-" + strings.Repeat("z", 50) + "\n")
	b.WriteString("after: limit\n")
	netWrite(t, dir, "big.yaml", b.String())
	res, out, err := netRead(t)
	if err != nil {
		t.Fatal(err)
	}
	f := netFile(t, res, "big.yaml")
	if !f.Truncated || f.Error != "" {
		t.Errorf("not truncated: error %q truncated %v", f.Error, f.Truncated)
	}
	if strings.Contains(out, netSecret[:8]) || strings.Contains(out, "cok-") {
		t.Error("part of a secret on the cut line was returned")
	}
	if len(f.Content) > netplanMaxFile || strings.Contains(f.Content, "after: limit") {
		t.Errorf("content length %d", len(f.Content))
	}
	if f.Size != int64(b.Len()) {
		t.Errorf("size %d want %d", f.Size, b.Len())
	}
}

func TestNetplanReadLimitsFileCount(t *testing.T) {
	dir := t.TempDir()
	netUseDir(t, dir)
	for i := 0; i < netplanMaxFiles+7; i++ {
		netWrite(t, dir, "f"+strings.Repeat("0", 3-len(itoaNet(i)))+itoaNet(i)+".yaml", "network: {version: 2}\n")
	}
	res, _, err := netRead(t)
	if err != nil || len(res.Files) != netplanMaxFiles {
		t.Errorf("got %d files, %v", len(res.Files), err)
	}
	if res.Files[0].Name != "f000.yaml" {
		t.Errorf("first file %s", res.Files[0].Name)
	}
}

func itoaNet(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func TestNetplanReadUnreadableDirectory(t *testing.T) {
	// The directory path is a regular file: reading fails with something
	// other than "does not exist".
	dir := t.TempDir()
	netWrite(t, dir, "file", "x")
	netUseDir(t, filepath.Join(dir, "file"))
	_, out, err := netRead(t)
	if err == nil || out != "" {
		t.Fatalf("got %q %v", out, err)
	}
	if ue, ok := err.(*UserError); !ok || ue.Message == "" || strings.Contains(ue.Message, dir) {
		t.Errorf("error %v is not a clean user message", err)
	}
}
