package apps

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMigrationCreatesTables(t *testing.T) {
	e := newTestEnv(t, "tcp://127.0.0.1:1", nil)
	want := map[string][]string{
		"apps_installed": {"slug", "name", "version", "config", "inputs", "images", "installed_at", "updated_at"},
		"apps_retained":  {"slug", "inputs", "removed_at"},
	}
	for table, cols := range want {
		rows, err := e.db.Query(`SELECT name, pk FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		pk := ""
		for rows.Next() {
			var name string
			var isPK int
			if err := rows.Scan(&name, &isPK); err != nil {
				t.Fatal(err)
			}
			got[name] = true
			if isPK > 0 {
				pk += name
			}
		}
		rows.Close()
		if len(got) == 0 {
			t.Fatalf("table %s does not exist", table)
		}
		for _, c := range cols {
			if !got[c] {
				t.Errorf("%s.%s is missing", table, c)
			}
		}
		if pk != "slug" {
			t.Errorf("%s: primary key %q, want slug", table, pk)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	e := newTestEnv(t, "tcp://127.0.0.1:1", nil)
	s := e.mod.store
	ctx := context.Background()

	if it, err := s.get(ctx, "tek"); err != nil || it != nil {
		t.Fatalf("empty store: %v %v", it, err)
	}
	if list, err := s.list(ctx); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}

	m := mustParse(t, manifestTek)
	cfg, in := mustResolve(t, m, Inputs{
		Env: map[string]string{"ADMIN_PASSWORD": "p@ss \"quoted\" ğüşıöç"}, Paths: map[string]string{"media": "/data/m"},
		Options: map[string]bool{"rawnet": true}, BindAddress: BindLoopback, Ports: map[string]int{"web": 9000},
	}, nil)
	images := map[string]ImageInfo{"app": {Image: "example/tek:1.0", ID: "sha256:abc", Digest: "example/tek@sha256:def"}}
	if err := s.save(ctx, cfg, in, images); err != nil {
		t.Fatal(err)
	}
	it, err := s.get(ctx, "tek")
	if err != nil || it == nil {
		t.Fatalf("get: %v", err)
	}
	if it.Slug != "tek" || it.Name != "Tek" || it.Version != "1.0" || it.InstalledAt == 0 || it.UpdatedAt != it.InstalledAt {
		t.Errorf("row: %+v", it)
	}
	if !sameConfig(it.Config, cfg) {
		a, _ := json.Marshal(it.Config)
		b, _ := json.Marshal(cfg)
		t.Errorf("configuration changed:\n%s\n%s", a, b)
	}
	a, _ := json.Marshal(it.Inputs)
	b, _ := json.Marshal(in)
	if string(a) != string(b) {
		t.Errorf("inputs changed:\n%s\n%s", a, b)
	}
	if it.Images["app"] != images["app"] {
		t.Errorf("images: %+v", it.Images)
	}

	// Saving again updates the row and keeps the installation time.
	if _, err := e.db.Exec(`UPDATE apps_installed SET installed_at = 1000, updated_at = 1000`); err != nil {
		t.Fatal(err)
	}
	cfg.Version = "2.0"
	if err := s.save(ctx, cfg, in, nil); err != nil {
		t.Fatal(err)
	}
	it, _ = s.get(ctx, "tek")
	if it.InstalledAt != 1000 || it.UpdatedAt == 1000 || it.Version != "2.0" || len(it.Images) != 0 {
		t.Errorf("after update: %+v", it)
	}
	cift, cin := mustResolve(t, mustParse(t, manifestCift), Inputs{}, nil)
	cift.Name = "alfa" // ordering ignores case
	if err := s.save(ctx, cift, cin, nil); err != nil {
		t.Fatal(err)
	}
	list, _ := s.list(ctx)
	if len(list) != 2 || list[0].Slug != "cift" || list[1].Slug != "tek" {
		t.Errorf("list is not ordered by name: %+v", list)
	}
	if err := s.remove(ctx, "tek"); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.get(ctx, "tek"); it != nil {
		t.Error("row still there after remove")
	}
	if it, _ := s.get(ctx, "cift"); it == nil {
		t.Error("remove deleted another application")
	}
}

func TestStoreRetainedValues(t *testing.T) {
	e := newTestEnv(t, "tcp://127.0.0.1:1", nil)
	s := e.mod.store
	ctx := context.Background()
	if in, err := s.retained(ctx, "tek"); err != nil || in != nil {
		t.Fatalf("nothing retained yet: %v %v", in, err)
	}
	in := &Inputs{Env: map[string]string{"DB_PASSWORD": "abc"}, Ports: map[string]int{"web": 1}}
	if err := s.retain(ctx, "tek", in); err != nil {
		t.Fatal(err)
	}
	in.Env["DB_PASSWORD"] = "def"
	if err := s.retain(ctx, "tek", in); err != nil {
		t.Fatalf("retaining twice: %v", err)
	}
	got, err := s.retained(ctx, "tek")
	if err != nil || got == nil || got.Env["DB_PASSWORD"] != "def" || got.Ports["web"] != 1 {
		t.Fatalf("retained: %+v %v", got, err)
	}
	if got.Paths == nil || got.Options == nil {
		t.Error("maps must be filled")
	}
	if err := s.dropRetained(ctx, "tek"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.retained(ctx, "tek"); got != nil {
		t.Error("retained values still there")
	}
}

// Rows written before the bind address existed read as "all".
func TestStoreReadsLegacyRows(t *testing.T) {
	e := newTestEnv(t, "tcp://127.0.0.1:1", nil)
	m := mustParse(t, manifestTek)
	cfg, in := mustResolve(t, m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}}, nil)
	rawCfg, _ := json.Marshal(legacy(t, cfg))
	var tree map[string]any
	_ = json.Unmarshal(rawCfg, &tree)
	delete(tree, "bind_address")
	for _, s := range tree["services"].([]any) {
		for _, p := range s.(map[string]any)["ports"].([]any) {
			delete(p.(map[string]any), "host_ip")
		}
	}
	rawCfg, _ = json.Marshal(tree)
	in.BindAddress = ""
	rawIn, _ := json.Marshal(in)
	var inTree map[string]any
	_ = json.Unmarshal(rawIn, &inTree)
	delete(inTree, "bind_address")
	delete(inTree, "options")
	rawIn, _ = json.Marshal(inTree)
	if _, err := e.db.Exec(`INSERT INTO apps_installed (slug, name, version, config, inputs, images, installed_at, updated_at)
		VALUES ('tek', 'Tek', '1.0', ?, ?, '{}', 1, 1)`, string(rawCfg), string(rawIn)); err != nil {
		t.Fatal(err)
	}
	it, err := e.mod.store.get(context.Background(), "tek")
	if err != nil {
		t.Fatal(err)
	}
	if it.Config.BindAddress != BindAll || it.Inputs.BindAddress != BindAll {
		t.Errorf("bind address %q / %q", it.Config.BindAddress, it.Inputs.BindAddress)
	}
	if ip := it.Config.Services[0].Ports[0].HostIP; ip != "0.0.0.0" {
		t.Errorf("host ip %q", ip)
	}
	if it.Inputs.Options == nil {
		t.Error("options map must be filled")
	}
	if !sameConfig(it.Config, cfg) {
		t.Error("a legacy row must read as the configuration the manifest produces today")
	}
}
