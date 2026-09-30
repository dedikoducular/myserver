package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"myserver/internal/security/fwcheck"
)

// These tests never run ufw. They rely on ufw being absent (as in the test
// container): an argument that passes validation ends at "UFW kurulu
// değil.", which is how accepted and refused arguments are told apart.

const fwNotInstalledMsg = "UFW kurulu değil."

func fwRequireNoUfw(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(fwcheck.UfwPath); err == nil {
		t.Skip("ufw is installed on this machine; these tests must not run it")
	}
}

func fwRun(t *testing.T, action string, args ...string) (string, error) {
	t.Helper()
	a, ok := actions[action]
	if !ok {
		t.Fatalf("action %q is not registered", action)
	}
	var out bytes.Buffer
	err := a(context.Background(), args, strings.NewReader(""), &out)
	return out.String(), err
}

func fwUserMessage(err error) string {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Message
	}
	return ""
}

func TestFirewallActionsRegistered(t *testing.T) {
	for _, n := range []string{"firewall-status", "firewall-enable", "firewall-disable", "firewall-rule-add", "firewall-rule-delete"} {
		if _, ok := actions[n]; !ok {
			t.Errorf("%s missing", n)
		}
	}
	for n := range actions {
		if strings.HasPrefix(n, "firewall-") {
			switch n {
			case "firewall-status", "firewall-enable", "firewall-disable", "firewall-rule-add", "firewall-rule-delete":
			default:
				t.Errorf("unexpected firewall action %q: every action must be covered by a test", n)
			}
		}
	}
}

func TestFirewallArgumentCounts(t *testing.T) {
	fwRequireNoUfw(t)
	cases := map[string][][]string{
		"firewall-status":      {{"x"}, {"--help"}, {"", ""}},
		"firewall-enable":      {{"x"}, {"--force"}, {"reset"}},
		"firewall-disable":     {{"x"}, {"--force"}},
		"firewall-rule-add":    {{}, {"allow"}, {"allow", "22", "tcp", "any"}, {"allow", "22", "tcp", "any", "", "extra"}},
		"firewall-rule-delete": {{}, {"numbered"}, {"numbered", "1"}, {"numbered", "1", "x", "y"}},
	}
	for action, lists := range cases {
		for _, args := range lists {
			out, err := fwRun(t, action, args...)
			if err == nil {
				t.Errorf("%s %q accepted", action, args)
				continue
			}
			if fwUserMessage(err) == fwNotInstalledMsg {
				t.Errorf("%s %q passed validation", action, args)
			}
			if out != "" {
				t.Errorf("%s %q wrote output %q", action, args, out)
			}
		}
	}
}

func TestFirewallStatusWithoutUfw(t *testing.T) {
	fwRequireNoUfw(t)
	out, err := fwRun(t, "firewall-status")
	if err != nil {
		t.Fatal(err)
	}
	var st fwcheck.RawStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if st != (fwcheck.RawStatus{}) {
		t.Errorf("got %+v", st)
	}
	for _, a := range []string{"firewall-enable", "firewall-disable"} {
		if _, err := fwRun(t, a); fwUserMessage(err) != fwNotInstalledMsg {
			t.Errorf("%s: %v", a, err)
		}
	}
}

func TestFirewallRuleAddValidation(t *testing.T) {
	fwRequireNoUfw(t)
	valid := [][]string{
		{"allow", "22", "tcp", "any", ""},
		{"deny", "5000:5010", "udp", "192.168.1.0/24", "SMB (LAN)"},
		{"limit", "22", "any", "2001:db8::1", "x"},
		{"reject", "80", "tcp", "10.1.2.3", ""},
	}
	for _, args := range valid {
		if _, err := fwRun(t, "firewall-rule-add", args...); fwUserMessage(err) != fwNotInstalledMsg {
			t.Errorf("%q: valid arguments refused: %v", args, err)
		}
	}
	invalid := [][]string{
		{"", "22", "tcp", "any", ""},
		{"ALLOW", "22", "tcp", "any", ""},
		{"delete", "22", "tcp", "any", ""},
		{"--force", "22", "tcp", "any", ""},
		{"allow", "", "tcp", "any", ""},
		{"allow", "0", "tcp", "any", ""},
		{"allow", "65536", "tcp", "any", ""},
		{"allow", "90:80", "tcp", "any", ""},
		{"allow", "80:90", "any", "any", ""},
		{"allow", "ssh", "tcp", "any", ""},
		{"allow", "22/tcp", "tcp", "any", ""},
		{"allow", "-1", "tcp", "any", ""},
		{"allow", "22 --force", "tcp", "any", ""},
		{"allow", "22", "", "any", ""},
		{"allow", "22", "icmp", "any", ""},
		{"allow", "22", "--proto", "any", ""},
		{"allow", "22", "tcp", "", ""},
		{"allow", "22", "tcp", "example.com", ""},
		{"allow", "22", "tcp", "-o", ""},
		{"allow", "22", "tcp", "--foo", ""},
		{"allow", "22", "tcp", "192.168.1.0/24 to any", ""},
		{"allow", "22", "tcp", "0.0.0.0/0", ""},
		{"allow", "22", "tcp", "fe80::1%eth0", ""},
		{"allow", "22", "tcp", "any", "bad;comment"},
		{"allow", "22", "tcp", "any", "--dry-run"},
		{"allow", "22", "tcp", "any", "-x"},
		{"allow", "22", "tcp", "any", "a\nb"},
		{"allow", "22", "tcp", "any", "a'b"},
		{"allow", "22", "tcp", "any", " lead"},
		{"allow", "22", "tcp", "any", strings.Repeat("c", 65)},
		{"allow", "22", "tcp", "any", "türkçe"},
	}
	for _, args := range invalid {
		_, err := fwRun(t, "firewall-rule-add", args...)
		msg := fwUserMessage(err)
		if err == nil || msg == "" || msg == fwNotInstalledMsg {
			t.Errorf("%q: not refused by validation (%v)", args, err)
		}
	}
}

func TestFirewallRuleDeleteValidation(t *testing.T) {
	fwRequireNoUfw(t)
	valid := [][]string{
		{"numbered", "1", "22/tcp ALLOW IN Anywhere"},
		{"added", "100000", "ufw allow 22/tcp"},
	}
	for _, args := range valid {
		if _, err := fwRun(t, "firewall-rule-delete", args...); fwUserMessage(err) != fwNotInstalledMsg {
			t.Errorf("%q: valid arguments refused: %v", args, err)
		}
	}
	id := "22/tcp ALLOW IN Anywhere"
	invalid := [][]string{
		{"", "1", id},
		{"status", "1", id},
		{"Numbered", "1", id},
		{"--force", "1", id},
		{"numbered", "", id},
		{"numbered", "0", id},
		{"numbered", "-1", id},
		{"numbered", "+1", id},
		{"numbered", "01", id},
		{"numbered", " 1", id},
		{"numbered", "1 ", id},
		{"numbered", "1.0", id},
		{"numbered", "0x1", id},
		{"numbered", "100001", id},
		{"numbered", "99999999999999999999", id},
		{"numbered", "--force", id},
		{"numbered", "1", ""},
		{"numbered", "1", id + "\n"},
		{"numbered", "1", id + "\r"},
		{"numbered", "1", "a\x00b"},
		{"numbered", "1", strings.Repeat("a", 401)},
	}
	for _, args := range invalid {
		_, err := fwRun(t, "firewall-rule-delete", args...)
		msg := fwUserMessage(err)
		if err == nil || msg == "" || msg == fwNotInstalledMsg {
			t.Errorf("%q: not refused by validation (%v)", args, err)
		}
	}
}

// Deleting by number only happens when the rule at that number still has
// the text the user saw. Fixtures are written from knowledge of ufw's output
// format, not captured from a live system.
func TestRuleMatches(t *testing.T) {
	before := fwcheck.ParseNumbered(`Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 445/tcp                    ALLOW IN    192.168.1.0/24             # SMB
[ 3] 8080/tcp                   ALLOW IN    Anywhere
`)
	// Rule 1 has been deleted by someone else: everything moved up.
	after := fwcheck.ParseNumbered(`Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 445/tcp                    ALLOW IN    192.168.1.0/24             # SMB
[ 2] 8080/tcp                   ALLOW IN    Anywhere
`)
	smb := "445/tcp ALLOW IN 192.168.1.0/24 # SMB"
	if !ruleMatches(before, 2, smb) {
		t.Fatal("unchanged list refused")
	}
	if ruleMatches(after, 2, smb) {
		t.Error("rule 2 is now another rule and must not be deleted")
	}
	if !ruleMatches(after, 1, smb) {
		t.Error("the rule at its new number must match")
	}
	if ruleMatches(after, 3, "8080/tcp ALLOW IN Anywhere") {
		t.Error("number beyond the list matched")
	}
	if ruleMatches(before, 2, "445/tcp ALLOW IN 192.168.1.0/24") || ruleMatches(before, 2, "") || ruleMatches(nil, 1, smb) {
		t.Error("different text matched")
	}
	if ruleMatches(fwcheck.ParseNumbered("Status: inactive\n"), 1, smb) {
		t.Error("inactive firewall matched")
	}

	added := fwcheck.ParseAdded("Added user rules (see 'ufw status' for running firewall):\nufw allow 22/tcp\nufw allow from 192.168.1.0/24 to any port 445 proto tcp comment 'SMB'\n")
	if !ruleMatches(added, 2, "ufw allow from 192.168.1.0/24 to any port 445 proto tcp comment 'SMB'") {
		t.Error("added rule not matched")
	}
	if ruleMatches(added, 1, "ufw allow from 192.168.1.0/24 to any port 445 proto tcp comment 'SMB'") {
		t.Error("added rule matched at the wrong position")
	}
}
