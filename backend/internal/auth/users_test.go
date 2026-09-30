package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestUserEndpointsRefuseNonAdmin(t *testing.T) {
	e, _, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	cases := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/auth/users", nil},
		{"POST", "/api/v1/auth/users", map[string]string{"username": "yeni", "password": testPassword, "role": "admin"}},
		{"PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"role": "admin"}},
		{"PUT", "/api/v1/auth/users/1", map[string]any{"password": "ele-gecirildi-123"}},
		{"DELETE", "/api/v1/auth/users/1", nil},
	}
	const want = `{"success":false,"data":null,"error":{"code":"forbidden","message":"Yetkiniz bulunmuyor."}}` + "\n"
	for _, c := range cases {
		res := e.do(u, c.method, c.path, c.body)
		if res.Status != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", c.method, c.path, res.Status)
		}
		if res.Raw != want {
			t.Errorf("%s %s: body %q", c.method, c.path, res.Raw)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 2 {
		t.Errorf("user count is %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE username = 'calisan' AND role = 'user'`); n != 1 {
		t.Error("the user promoted themselves")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
		t.Error("the administrator's password was changed by a normal user")
	}
	// Things a normal user may do.
	if res := e.do(u, "GET", "/api/v1/auth/sessions", nil); res.Status != http.StatusOK {
		t.Errorf("own sessions: %d", res.Status)
	}
}

func TestUsersListHidesHashes(t *testing.T) {
	e, admin, _ := adminAndUser(t)
	res := e.do(admin, "GET", "/api/v1/auth/users", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status %d", res.Status)
	}
	if strings.Contains(res.Raw, "argon2") || strings.Contains(res.Raw, "password") {
		t.Fatalf("user list exposes password data: %s", res.Raw)
	}
	var users []User
	if err := json.Unmarshal(res.Data, &users); err != nil || len(users) != 2 {
		t.Fatalf("users: %v %s", err, res.Raw)
	}
}

func TestCreateUser(t *testing.T) {
	e, admin, _ := adminAndUser(t)
	res := e.do(admin, "POST", "/api/v1/auth/users", map[string]string{"username": "Yeni_Kisi", "password": testPassword, "role": "user"})
	if res.Status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, testPassword) || strings.Contains(res.Raw, "argon2") {
		t.Fatalf("response leaks the password: %s", res.Raw)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE username = 'yeni_kisi' AND role = 'user' AND disabled = 0`); n != 1 {
		t.Fatal("user not stored in normalized form")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("yeni_kisi", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("new user cannot log in: %d", r.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'users.create' AND target = 'yeni_kisi' AND username = 'admin'`); n != 1 {
		t.Fatal("creation not audited")
	}
}

func TestCreateUserRefusals(t *testing.T) {
	e, admin, _ := adminAndUser(t)
	cases := map[string]struct {
		body any
		want int
	}{
		"duplicate":            {map[string]string{"username": "calisan", "password": testPassword, "role": "user"}, 409},
		"duplicate other case": {map[string]string{"username": "CALISAN", "password": testPassword, "role": "user"}, 409},
		"duplicate mixed case": {map[string]string{"username": " Calisan ", "password": testPassword, "role": "user"}, 409},
		"duplicate admin":      {map[string]string{"username": "Admin", "password": testPassword, "role": "user"}, 409},
		"root":                 {map[string]string{"username": "root", "password": testPassword, "role": "user"}, 400},
		"root other case":      {map[string]string{"username": "ROOT", "password": testPassword, "role": "user"}, 400},
		"short name":           {map[string]string{"username": "ab", "password": testPassword, "role": "user"}, 400},
		"bad characters":       {map[string]string{"username": "a b;c", "password": testPassword, "role": "user"}, 400},
		"weak password":        {map[string]string{"username": "yeni", "password": "kisa", "role": "user"}, 400},
		"password is name":     {map[string]string{"username": "uzunkullanici", "password": "UzunKullanici", "role": "user"}, 400},
		"no role":              {map[string]string{"username": "yeni", "password": testPassword}, 400},
		"unknown role":         {map[string]string{"username": "yeni", "password": testPassword, "role": "superuser"}, 400},
		"role other case":      {map[string]string{"username": "yeni", "password": testPassword, "role": "Admin"}, 400},
		"unknown field":        {map[string]any{"username": "yeni", "password": testPassword, "role": "user", "id": 1}, 400},
		"disabled field":       {map[string]any{"username": "yeni", "password": testPassword, "role": "user", "disabled": false}, 400},
	}
	for name, c := range cases {
		res := e.do(admin, "POST", "/api/v1/auth/users", c.body)
		if res.Status != c.want || res.Success {
			t.Errorf("%s: status %d, want %d (%s)", name, res.Status, c.want, res.Raw)
		}
		if strings.Contains(res.Raw, "UNIQUE") || strings.Contains(res.Raw, "constraint") || strings.Contains(res.Raw, "sqlite") {
			t.Errorf("%s: database error text in the response: %s", name, res.Raw)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 2 {
		t.Fatalf("user count is %d after refused creations", n)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	e, admin, userID := adminAndUser(t)
	var adminID int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/auth/users/%d", adminID)
	cases := map[string]any{
		"disable":            map[string]any{"disabled": true},
		"demote":             map[string]any{"role": "user"},
		"demote and disable": map[string]any{"role": "user", "disabled": true},
	}
	for name, body := range cases {
		res := e.do(admin, "PUT", path, body)
		if res.Status != http.StatusConflict {
			t.Errorf("%s the last admin: status %d, want 409 (%s)", name, res.Status, res.Raw)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE id = ? AND role = 'admin' AND disabled = 0`, adminID); n != 1 {
		t.Fatal("the last admin was changed")
	}

	// A disabled second admin does not count as a remaining admin.
	second := e.addUser("ikinci", testPassword, RoleAdmin)
	if _, err := e.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, second); err != nil {
		t.Fatal(err)
	}
	for name, body := range cases {
		res := e.do(admin, "PUT", path, body)
		if res.Status != http.StatusConflict {
			t.Errorf("%s the last active admin (a disabled admin exists): status %d, want 409", name, res.Status)
		}
	}
	// Deleting the disabled admin and the normal user is fine...
	if res := e.do(admin, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", second), nil); res.Status != http.StatusOK {
		t.Errorf("delete disabled admin: %d %s", res.Status, res.Raw)
	}
	if res := e.do(admin, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", userID), nil); res.Status != http.StatusOK {
		t.Errorf("delete user: %d", res.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`); n != 1 {
		t.Fatalf("%d active admins left", n)
	}
}

func TestLastAdminCannotBeDeletedByAnotherSession(t *testing.T) {
	e := ready(t)
	// "ikinci" is an admin; "admin" is then disabled, leaving one active
	// admin, who is deleted by nobody: not by themselves, and the disabled
	// one cannot act at all.
	second := e.addUser("ikinci", testPassword, RoleAdmin)
	a := e.login("admin", testPassword, "")
	b := e.login("ikinci", testPassword, "")
	var adminID int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	// With two active admins, one may remove the other.
	if res := e.do(b, "PUT", fmt.Sprintf("/api/v1/auth/users/%d", adminID), map[string]any{"role": "user"}); res.Status != http.StatusOK {
		t.Fatalf("demote with two admins: %d %s", res.Status, res.Raw)
	}
	// "admin" (now a user, old session ended) cannot touch the last admin.
	if e.alive(a) {
		t.Fatal("demoted admin kept the session")
	}
	a = e.login("admin", testPassword, "")
	if res := e.do(a, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", second), nil); res.Status != http.StatusForbidden {
		t.Fatalf("demoted admin deleting the last admin: %d", res.Status)
	}
	// And the last admin cannot remove themselves in any way.
	self := fmt.Sprintf("/api/v1/auth/users/%d", second)
	if res := e.do(b, "DELETE", self, nil); res.Status != http.StatusConflict {
		t.Errorf("self delete: %d", res.Status)
	}
	if res := e.do(b, "PUT", self, map[string]any{"disabled": true}); res.Status != http.StatusConflict {
		t.Errorf("self disable: %d", res.Status)
	}
	if res := e.do(b, "PUT", self, map[string]any{"role": "user"}); res.Status != http.StatusConflict {
		t.Errorf("self demote: %d", res.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`); n != 1 {
		t.Fatalf("%d active admins", n)
	}
}

func TestDeleteLastAdminRefused(t *testing.T) {
	e := ready(t)
	second := e.addUser("ikinci", testPassword, RoleAdmin)
	b := e.login("ikinci", testPassword, "")
	var adminID int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	// Deleting the other admin leaves the caller: allowed.
	if res := e.do(b, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", adminID), nil); res.Status != http.StatusOK {
		t.Fatalf("delete other admin: %d %s", res.Status, res.Raw)
	}
	if res := e.do(b, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", second), nil); res.Status != http.StatusConflict {
		t.Fatalf("delete self as last admin: %d", res.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Fatalf("%d users left", n)
	}
}

func TestUserCannotDeleteSelf(t *testing.T) {
	e := ready(t)
	e.addUser("ikinci", testPassword, RoleAdmin) // so the last-admin rule is not what refuses
	a := e.login("admin", testPassword, "")
	var adminID int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	res := e.do(a, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", adminID), nil)
	if res.Status != http.StatusConflict {
		t.Fatalf("self delete: status %d, want 409 (%s)", res.Status, res.Raw)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE id = ?`, adminID); n != 1 {
		t.Fatal("the user deleted themselves")
	}
}

func TestUserUpdateAndDeleteInputValidation(t *testing.T) {
	e, admin, id := adminAndUser(t)
	path := fmt.Sprintf("/api/v1/auth/users/%d", id)
	bad := map[string]struct {
		method, path string
		body         any
		want         int
	}{
		"unknown user update":  {"PUT", "/api/v1/auth/users/9999", map[string]any{"role": "user"}, 404},
		"unknown user delete":  {"DELETE", "/api/v1/auth/users/9999", nil, 404},
		"non numeric id":       {"PUT", "/api/v1/auth/users/abc", map[string]any{"role": "user"}, 400},
		"sql in id":            {"DELETE", "/api/v1/auth/users/1%20OR%201=1", nil, 400},
		"invalid role":         {"PUT", path, map[string]any{"role": "root"}, 400},
		"weak password":        {"PUT", path, map[string]any{"password": "kisa"}, 400},
		"rename attempt":       {"PUT", path, map[string]any{"username": "admin2"}, 400},
		"wrong type":           {"PUT", path, map[string]any{"disabled": "yes"}, 400},
		"invalid role + valid": {"PUT", path, map[string]any{"role": "root", "disabled": true}, 400},
	}
	for name, c := range bad {
		res := e.do(admin, c.method, c.path, c.body)
		if res.Status != c.want {
			t.Errorf("%s: status %d, want %d (%s)", name, res.Status, c.want, res.Raw)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE id = ? AND role = 'user' AND disabled = 0`, id); n != 1 {
		t.Fatal("a refused update changed the user")
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 2 {
		t.Fatalf("user count %d", n)
	}
}
