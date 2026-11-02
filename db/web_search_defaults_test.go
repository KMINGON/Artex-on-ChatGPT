package db

import "testing"

func assertWebSearchDefaults(t *testing.T, d *DB, settings map[string]string, enabledAgents map[string]bool) {
	t.Helper()
	for key, want := range settings {
		got, found, err := d.GetSetting(key)
		if err != nil || !found || got != want {
			t.Fatalf("setting %s=%q found=%v, want %q: %v", key, got, found, want, err)
		}
	}
	for _, key := range []string{"goals", "planner", "mainagent", "worker", "auto", "pentest"} {
		var got bool
		if err := d.QueryRow(`SELECT web_search FROM agents WHERE key=$1`, key).Scan(&got); err != nil || got != enabledAgents[key] {
			t.Fatalf("agent %s web_search=%v, want %v: %v", key, got, enabledAgents[key], err)
		}
	}
}

func TestBuiltinWebSearchDefaultsNewInstallAndRestart(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	assertWebSearchDefaults(t, d, map[string]string{
		"web_search_enabled": "true", "web_search_backend": "ddgs",
	}, map[string]bool{"worker": true, "pentest": true})
	// Runtime/UI choices must survive opening the database again.
	want := map[string]string{
		"web_search_enabled": "false", "web_search_backend": "tavily",
		"tavily_search_api_key": "fixture-key", "web_search_proxy": "http://fixture.invalid:8080",
	}
	for key, value := range want {
		if err := d.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`UPDATE agents SET web_search=key='auto'`); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertWebSearchDefaults(t, reopened, want, map[string]bool{"auto": true})
}

func TestBuiltinWebSearchDefaultsPreserveExistingInstallations(t *testing.T) {
	for _, setting := range []string{"missing", "false", "true"} {
		t.Run(setting, func(t *testing.T) {
			d, dsn := templateFixtureDB(t)
			// Recreate an existing version without the new initialization marker.
			if _, err := d.Exec(`DELETE FROM settings WHERE key IN ('builtin_web_search_defaults_v1','web_search_enabled','web_search_backend');
UPDATE agents SET web_search=key IN ('worker','auto')`); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"brave_search_api_key": "fixture-key", "web_search_proxy": "socks5://fixture.invalid:1080"}
			if setting != "missing" {
				want["web_search_enabled"] = setting
				want["web_search_backend"] = "brave-free"
			}
			for key, value := range want {
				if err := d.SetSetting(key, value); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := Open(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertWebSearchDefaults(t, reopened, want, map[string]bool{"worker": true, "auto": true})
			if setting == "missing" {
				for _, key := range []string{"web_search_enabled", "web_search_backend"} {
					if _, found, err := reopened.GetSetting(key); err != nil || found {
						t.Fatalf("previously unset %s acquired a default: found=%v err=%v", key, found, err)
					}
				}
			}
		})
	}
}

func TestBuiltinWebSearchDefaultsDoNotRestoreDeletedSettings(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	if _, err := d.Exec(`DELETE FROM agents WHERE builtin;
DELETE FROM settings WHERE key IN ('web_search_enabled','web_search_backend')`); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertWebSearchDefaults(t, reopened, nil, nil)
	for _, key := range []string{"web_search_enabled", "web_search_backend"} {
		if _, found, err := reopened.GetSetting(key); err != nil || found {
			t.Fatalf("deleted %s was restored: found=%v err=%v", key, found, err)
		}
	}
}

func TestBuiltinWebSearchDefaultsPreservePreconfiguredValues(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if _, err := d.Exec(`DELETE FROM agents WHERE key<>'pentest';
UPDATE agents SET builtin=false,web_search=false WHERE key='pentest';
DELETE FROM settings WHERE key='builtin_web_search_defaults_v1';
UPDATE settings SET value='false' WHERE key='web_search_enabled';
UPDATE settings SET value='tavily' WHERE key='web_search_backend'`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	assertWebSearchDefaults(t, d, map[string]string{
		"web_search_enabled": "false", "web_search_backend": "tavily",
	}, map[string]bool{"worker": true})
}

func TestBuiltinWebSearchDefaultsRollbackWithAgentInitialization(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if _, err := d.Exec(`DELETE FROM agents;
DELETE FROM settings WHERE key IN ('builtin_web_search_defaults_v1','web_search_enabled','web_search_backend');
CREATE FUNCTION reject_search_agent_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.key='pentest' THEN RAISE EXCEPTION 'fixture agent seed failure'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER reject_search_agent_fixture BEFORE INSERT ON agents
FOR EACH ROW EXECUTE FUNCTION reject_search_agent_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedBuiltins(); err == nil {
		t.Fatal("expected agent initialization to fail")
	}
	var agents, settings int
	if err := d.QueryRow(`SELECT (SELECT count(*) FROM agents),
(SELECT count(*) FROM settings WHERE key IN ('builtin_web_search_defaults_v1','web_search_enabled','web_search_backend'))`).Scan(&agents, &settings); err != nil {
		t.Fatal(err)
	}
	if agents != 0 || settings != 0 {
		t.Fatalf("failed initialization partly committed: agents=%d settings=%d", agents, settings)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_search_agent_fixture ON agents`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	assertWebSearchDefaults(t, d, map[string]string{
		"web_search_enabled": "true", "web_search_backend": "ddgs",
	}, map[string]bool{"worker": true, "pentest": true})
}
