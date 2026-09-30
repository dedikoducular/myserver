package netcheck

import (
	"strings"
	"testing"
)

// A syntactically valid WireGuard key (44 base64 characters); made up.
const wgKey = "4GgaQCy68nzNsUE5aJ9fuLzHhB65tAlwbmA72MWnOm8="
const wgKey2 = "M9nt4YujIOmNrRmpIRTmYSfMdrpvE7u5WkX8z2fNLkE="

type redactCase struct {
	name    string
	in      string
	secrets []string // must not appear in the output
	keep    []string // must still appear in the output
}

func runRedact(t *testing.T, cases []redactCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, n := RedactNetplan(c.in)
			for _, s := range c.secrets {
				if strings.Contains(out, s) {
					t.Errorf("secret %q leaked.\ninput:\n%s\noutput:\n%s", s, c.in, out)
				}
			}
			for _, k := range c.keep {
				if !strings.Contains(out, k) {
					t.Errorf("%q was lost.\ninput:\n%s\noutput:\n%s", k, c.in, out)
				}
			}
			if len(c.secrets) > 0 && n == 0 {
				t.Errorf("redaction count is 0")
			}
			if len(c.secrets) > 0 && !strings.Contains(out, RedactionMask) {
				t.Errorf("mask missing from output:\n%s", out)
			}
		})
	}
}

func TestRedactNetplanBlockStyle(t *testing.T) {
	runRedact(t, []redactCase{
		{"plain", "network:\n  wifis:\n    wlan0:\n      access-points:\n        home:\n          password: hunter2secret\n      dhcp4: true\n",
			[]string{"hunter2secret"}, []string{"wlan0:", "home:", "dhcp4: true", "password:"}},
		{"double quoted", "          password: \"s3cr3t pass\"\n", []string{"s3cr3t pass"}, nil},
		{"single quoted", "          password: 's3cr3t pass'\n", []string{"s3cr3t pass"}, nil},
		{"quoted key", "          \"password\": s3cr3tvalue\n", []string{"s3cr3tvalue"}, nil},
		{"single quoted key", "          'password': s3cr3tvalue\n", []string{"s3cr3tvalue"}, nil},
		{"no space after colon value", "          password:    s3cr3tvalue   \n", []string{"s3cr3tvalue"}, nil},
		{"tab indentation", "\t\tpassword:\ts3cr3tvalue\n", []string{"s3cr3tvalue"}, nil},
		{"crlf", "wifis:\r\n  password: s3cr3tvalue\r\n  dhcp4: true\r\n", []string{"s3cr3tvalue"}, []string{"dhcp4: true"}},
		{"upper case key", "  Password: s3cr3tvalue\n", []string{"s3cr3tvalue"}, nil},
		{"same line comment", "          password: s3cr3tvalue # wifi at home\n", []string{"s3cr3tvalue"}, nil},
		{"same line comment quoted", "          password: \"s3cr3t#value\" # note\n", []string{"s3cr3t", "#value"}, nil},
		{"comment then value below", "          password: # see below\n            s3cr3tvalue\n          mode: infrastructure\n",
			[]string{"s3cr3tvalue"}, []string{"mode: infrastructure"}},
		{"psk", "      psk: s3cr3tvalue\n", []string{"s3cr3tvalue"}, nil},
		{"8021x", "      auth:\n        key-management: eap\n        method: ttls\n        identity: user@example.com\n        password: s3cr3tvalue\n        client-key: /etc/ssl/client.key\n        client-key-password: k3ypassw0rd\n        ca-certificate: /etc/ssl/ca.pem\n",
			[]string{"s3cr3tvalue", "k3ypassw0rd"}, []string{"key-management: eap", "identity: user@example.com", "ca-certificate: /etc/ssl/ca.pem"}},
		{"list item", "  - password: s3cr3tvalue\n    name: keepme\n", []string{"s3cr3tvalue"}, []string{"name: keepme"}},
		{"anchor and tag", "  password: !!str s3cr3tvalue\n", []string{"s3cr3tvalue"}, nil},
		{"multi-line double quoted", "  password: \"first-part\n    s3cond-part\"\n  dhcp4: true\n",
			[]string{"first-part", "s3cond-part"}, []string{"dhcp4: true"}},
		{"multi-line plain", "  password: first-part\n    s3cond-part\n  dhcp4: true\n",
			[]string{"first-part", "s3cond-part"}, []string{"dhcp4: true"}},
	})
}

func TestRedactNetplanBlockScalars(t *testing.T) {
	runRedact(t, []redactCase{
		{"literal", "      password: |\n        s3cr3t-line-1\n        s3cr3t-line-2\n      dhcp4: true\n",
			[]string{"s3cr3t-line-1", "s3cr3t-line-2"}, []string{"dhcp4: true"}},
		{"folded strip", "      password: >-\n        s3cr3t-line-1\n\n        s3cr3t-line-2\n      dhcp4: true\n",
			[]string{"s3cr3t-line-1", "s3cr3t-line-2"}, []string{"dhcp4: true"}},
		{"literal with indent indicator", "      password: |2+\n        s3cr3t-line-1\n      dhcp4: true\n",
			[]string{"s3cr3t-line-1"}, []string{"dhcp4: true"}},
		{"literal with comment", "      password: | # the wifi password\n        s3cr3t-line-1\n      dhcp4: true\n",
			[]string{"s3cr3t-line-1"}, []string{"dhcp4: true"}},
		{"at end of file without newline", "      password: |\n        s3cr3t-line-1", []string{"s3cr3t-line-1"}, nil},
		{"in list item keeps sibling", "  - password: |\n      s3cr3t-line-1\n    name: keepme\n",
			[]string{"s3cr3t-line-1"}, []string{"name: keepme"}},
		{"nested mapping under secret key", "      private:\n        value: s3cr3tvalue\n      mtu: 1420\n",
			[]string{"s3cr3tvalue"}, []string{"mtu: 1420"}},
	})
}

func TestRedactNetplanFlowStyle(t *testing.T) {
	runRedact(t, []redactCase{
		{"simple", "        home: {password: s3cr3tvalue}\n", []string{"s3cr3tvalue"}, []string{"home:"}},
		{"quoted value with separators", "        home: {password: \"s3cr3t, va}lue\", mode: infrastructure}\n",
			[]string{"s3cr3t", "va}lue"}, []string{"mode: infrastructure"}},
		{"single quoted", "        home: {mode: infrastructure, password: 's3cr3t value'}\n",
			[]string{"s3cr3t value"}, []string{"mode: infrastructure"}},
		{"json style", "        home: {\"password\": \"s3cr3tvalue\"}\n", []string{"s3cr3tvalue"}, nil},
		{"whole document", "network: {version: 2, wifis: {wlan0: {dhcp4: true, access-points: {home: {password: s3cr3tvalue}}}}}\n",
			[]string{"s3cr3tvalue"}, []string{"version: 2", "dhcp4: true"}},
		{"ssid with spaces", "        \"My Home WiFi\": {password: s3cr3tvalue}\n", []string{"s3cr3tvalue"}, []string{"My Home WiFi"}},
		{"ssid with spaces single quotes", "        'My Home WiFi': { password: \"s3cr3tvalue\" }\n", []string{"s3cr3tvalue"}, []string{"My Home WiFi"}},
		{"list item flow", "      - {name: keepme, password: s3cr3tvalue}\n", []string{"s3cr3tvalue"}, []string{"name: keepme"}},
		{"bare flow line", "        {password: s3cr3tvalue}\n", []string{"s3cr3tvalue"}, nil},
		{"flow with comment", "        home: {password: s3cr3tvalue} # my wifi\n", []string{"s3cr3tvalue"}, nil},
		{"flow over lines", "        home: {\n          mode: infrastructure,\n          password: s3cr3tvalue\n        }\n",
			[]string{"s3cr3tvalue"}, []string{"mode: infrastructure"}},
		{"flow continuation line", "        home: {mode: infrastructure,\n          password: s3cr3tvalue, band: 5GHz}\n",
			[]string{"s3cr3tvalue"}, []string{"mode: infrastructure"}},
		{"secret key holding a flow map", "        auth: {key-management: psk, password: s3cr3tvalue}\n",
			[]string{"s3cr3tvalue"}, []string{"key-management: psk"}},
		{"no space after colon json", "        home: {\"password\":\"s3cr3tvalue\"}\n", []string{"s3cr3tvalue"}, nil},
	})
}

func TestRedactNetplanWireGuard(t *testing.T) {
	runRedact(t, []redactCase{
		{"key", "  tunnels:\n    wg0:\n      mode: wireguard\n      key: " + wgKey + "\n      port: 51820\n",
			[]string{wgKey}, []string{"mode: wireguard", "port: 51820"}},
		{"private-key", "      private-key: " + wgKey + "\n", []string{wgKey}, nil},
		{"keys mapping", "      keys:\n        private: " + wgKey + "\n        shared: \"" + wgKey2 + "\"\n",
			[]string{wgKey, wgKey2}, nil},
		{"key file path is a secret key too", "      key: /etc/wireguard/private.key\n", []string{"/etc/wireguard/private.key"}, nil},
		{"bare key under unknown name", "      something-else: " + wgKey + "\n", []string{wgKey}, []string{"something-else:"}},
		{"key in flow", "      keys: {private: " + wgKey + ", shared: " + wgKey2 + "}\n", []string{wgKey, wgKey2}, nil},
		{"key in a comment", "      # old key was " + wgKey + "\n", []string{wgKey}, nil},
		{"key in a list", "      - " + wgKey + "\n", []string{wgKey}, nil},
	})
}

func TestRedactNetplanKeepsOrdinaryConfig(t *testing.T) {
	in := `# This is the network config written by 'subiquity'
network:
  version: 2
  renderer: networkd
  ethernets:
    enp3s0:
      dhcp4: false
      addresses: [192.168.1.10/24, "fd00::10/64"]
      routes:
        - to: default
          via: 192.168.1.1
      nameservers:
        addresses: [1.1.1.1, 8.8.8.8]
        search: [lan]
  tunnels:
    wg0:
      mode: wireguard
      keys:
        public: ` + wgKey2 + `
`
	out, n := RedactNetplan(in)
	// Only the (harmless) public key looks like a key and is masked.
	want := strings.Replace(in, wgKey2, RedactionMask, 1)
	if out != want {
		t.Errorf("ordinary configuration was changed.\ngot:\n%s\nwant:\n%s", out, want)
	}
	if n != 1 {
		t.Errorf("count %d want 1", n)
	}
	plain := "network:\n  version: 2\n  ethernets:\n    eth0: {dhcp4: true, optional: true}\n"
	if out, n := RedactNetplan(plain); out != plain || n != 0 {
		t.Errorf("plain file changed (%d):\n%s", n, out)
	}
}

func TestIsSecretKey(t *testing.T) {
	secret := []string{"password", "Password", "PASSWORD", "psk", "key", "private", "shared", "private-key",
		"private_key", "client-key-password", "client-key", "passphrase", "secret", "token", "wpa-psk", "pin",
		"private-key-flags", "auth-token"}
	for _, k := range secret {
		if !IsSecretKey(k) {
			t.Errorf("%q should be secret", k)
		}
	}
	plain := []string{"public", "public-key", "key-management", "dhcp4", "addresses", "mode", "keys", "identity",
		"ca-certificate", "endpoint", "allowed-ips", "keepalive", "spinner"}
	for _, k := range plain {
		if IsSecretKey(k) {
			t.Errorf("%q should not be secret", k)
		}
	}
}
