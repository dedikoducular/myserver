package auth

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHashPasswordFormatAndRoundTrip(t *testing.T) {
	h, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=2,p=2\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !re.MatchString(h) {
		t.Fatalf("hash is not the expected Argon2id PHC string: %q", h)
	}
	if strings.Contains(h, testPassword) {
		t.Fatal("hash contains the password")
	}
	ok, err := VerifyPassword(h, testPassword)
	if err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}
}

func TestVerifyPasswordWrong(t *testing.T) {
	h, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "x", testPassword + " ", strings.ToUpper(testPassword), testPassword[:len(testPassword)-1], testPassword + "\x00"} {
		ok, err := VerifyPassword(h, p)
		if err != nil {
			t.Errorf("%q: unexpected error %v", p, err)
		}
		if ok {
			t.Errorf("%q accepted as the password", p)
		}
	}
}

func TestHashPasswordSalted(t *testing.T) {
	a, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical")
	}
	pa, pb := strings.Split(a, "$"), strings.Split(b, "$")
	if pa[4] == pb[4] {
		t.Fatal("salt was reused")
	}
	if pa[5] == pb[5] {
		t.Fatal("derived keys are identical")
	}
}

func TestVerifyPasswordTamperedHash(t *testing.T) {
	h, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, "$")
	flip := func(s string) string {
		c := byte('A')
		if s[0] == 'A' {
			c = 'B'
		}
		return string(c) + s[1:]
	}
	salt := append([]string{}, parts...)
	salt[4] = flip(salt[4])
	key := append([]string{}, parts...)
	key[5] = flip(key[5])
	params := append([]string{}, parts...)
	params[3] = "m=65536,t=3,p=2"
	for name, enc := range map[string]string{"salt": strings.Join(salt, "$"), "key": strings.Join(key, "$"), "params": strings.Join(params, "$")} {
		ok, _ := VerifyPassword(enc, testPassword)
		if ok {
			t.Errorf("hash with modified %s still verifies", name)
		}
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	const salt = "c29tZXNhbHRzb21lc2FsdA"
	const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	cases := map[string]string{
		"empty":             "",
		"plain text":        testPassword,
		"no fields":         "$",
		"bcrypt":            "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"argon2i":           "$argon2i$v=19$m=65536,t=2,p=2$" + salt + "$" + key,
		"argon2d":           "$argon2d$v=19$m=65536,t=2,p=2$" + salt + "$" + key,
		"missing key":       "$argon2id$v=19$m=65536,t=2,p=2$" + salt,
		"empty key":         "$argon2id$v=19$m=65536,t=2,p=2$" + salt + "$",
		"extra field":       "$argon2id$v=19$m=65536,t=2,p=2$" + salt + "$" + key + "$x",
		"old version":       "$argon2id$v=16$m=65536,t=2,p=2$" + salt + "$" + key,
		"no version":        "$argon2id$$m=65536,t=2,p=2$" + salt + "$" + key,
		"bad params":        "$argon2id$v=19$m=abc,t=2,p=2$" + salt + "$" + key,
		"missing param":     "$argon2id$v=19$m=65536,t=2$" + salt + "$" + key,
		"zero memory":       "$argon2id$v=19$m=0,t=2,p=2$" + salt + "$" + key,
		"zero time":         "$argon2id$v=19$m=65536,t=0,p=2$" + salt + "$" + key,
		"zero threads":      "$argon2id$v=19$m=65536,t=2,p=0$" + salt + "$" + key,
		"negative memory":   "$argon2id$v=19$m=-65536,t=2,p=2$" + salt + "$" + key,
		"huge memory":       "$argon2id$v=19$m=4294967295,t=2,p=2$" + salt + "$" + key,
		"overflow memory":   "$argon2id$v=19$m=99999999999999,t=2,p=2$" + salt + "$" + key,
		"memory over limit": "$argon2id$v=19$m=1048577,t=2,p=2$" + salt + "$" + key,
		"huge time":         "$argon2id$v=19$m=65536,t=4294967295,p=2$" + salt + "$" + key,
		"time over limit":   "$argon2id$v=19$m=65536,t=17,p=2$" + salt + "$" + key,
		"overflow threads":  "$argon2id$v=19$m=65536,t=2,p=256$" + salt + "$" + key,
		"bad salt base64":   "$argon2id$v=19$m=65536,t=2,p=2$!!!!$" + key,
		"bad key base64":    "$argon2id$v=19$m=65536,t=2,p=2$" + salt + "$!!!!",
		"padded base64":     "$argon2id$v=19$m=65536,t=2,p=2$" + salt + "==$" + key,
	}
	for name, enc := range cases {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			ok, err := VerifyPassword(enc, testPassword)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			if ok {
				t.Fatal("malformed hash verified a password")
			}
			if err == nil {
				t.Fatal("malformed hash was not reported as an error")
			}
			// A rejected hash must never reach the key derivation: that
			// would allocate the memory the hash itself asks for.
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
				t.Fatalf("rejecting the hash allocated %d bytes", grew)
			}
			if elapsed > 2*time.Second {
				t.Fatalf("rejecting the hash took %v", elapsed)
			}
		})
	}
}

// A stored hash is attacker-influenced only through the database, but the
// accepted parameter range still bounds what one verification may cost.
func TestVerifyPasswordParameterCeiling(t *testing.T) {
	const salt = "c29tZXNhbHRzb21lc2FsdA"
	// 1 GiB is the documented ceiling; one KiB more must be refused.
	if _, err := VerifyPassword("$argon2id$v=19$m=1048577,t=1,p=1$"+salt+"$AAAA", "x"); err == nil {
		t.Fatal("memory parameter above the ceiling accepted")
	}
	// An oversized derived-key length is an allocation request as well.
	huge := strings.Repeat("A", 4*1024*1024)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ok, _ := VerifyPassword("$argon2id$v=19$m=8,t=1,p=1$"+salt+"$"+huge, "x")
	runtime.ReadMemStats(&after)
	if ok {
		t.Fatal("garbage hash verified")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("oversized key length allocated %d bytes", grew)
	}
}

func TestValidatePassword(t *testing.T) {
	bad := map[string]string{
		"empty":            "",
		"short":            "kisa12345",
		"nine multibyte":   "şşşşşşşşş",
		"same as username": "administrator",
		"same, other case": "ADMINISTRATOR",
		"too long":         strings.Repeat("a", 257),
	}
	for name, p := range bad {
		if err := validatePassword("administrator", p); err == nil {
			t.Errorf("%s: password %q accepted", name, p)
		}
	}
	for _, p := range []string{"on-karakter", "şşşşşşşşşş", strings.Repeat("a", 256)} {
		if err := validatePassword("administrator", p); err != nil {
			t.Errorf("password %q refused: %v", p, err)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	for _, u := range []string{"", "ab", "root", "Admin", "1abc", "-abc", "_abc", "a b c", "abc!", "abc/def", "abc\x00", "../etc",
		"ali\n", "çağrı", strings.Repeat("a", 33), "abc;rm", "abc$(id)"} {
		if err := validateUsername(u); err == nil {
			t.Errorf("username %q accepted", u)
		}
	}
	for _, u := range []string{"abc", "admin", "ali_veli-42", "rooter", strings.Repeat("a", 32)} {
		if err := validateUsername(u); err != nil {
			t.Errorf("username %q refused: %v", u, err)
		}
	}
}
