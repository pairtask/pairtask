//go:build linux

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func routeBrokerEnv(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"agent", "operator", "db", "pair", "egress"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0750); err != nil {
			t.Fatal(err)
		}
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"PAIRTASK_SOCKET_PATH":          filepath.Join(root, "agent", "agent.sock"),
		"PAIRTASK_OPERATOR_SOCKET_PATH": filepath.Join(root, "operator", "operator.sock"),
		"PAIRTASK_OPERATOR_UID":         strconv.Itoa(os.Geteuid()),
		"PAIRTASK_DB_PATH":              filepath.Join(root, "db", "broker.db"),
		"PAIRTASK_PAIRING_SOCKET":       filepath.Join(root, "pair", "pair.sock"),
		"PAIRTASK_PAIRING_SOCKET_GROUP": group.Name,
		"PAIRTASK_PAIRING_EGRESS":       "socket:" + filepath.Join(root, "egress", "egress.sock"),
	}
}

func readRouteConfig(env map[string]string) (config, error) {
	return loadConfig(func(key string) (string, bool) { v, found := env[key]; return v, found })
}

func TestPairingRouteAcceptanceConfigResolvesGroupAndSocket(t *testing.T) {
	env := routeBrokerEnv(t)
	cfg, err := readRouteConfig(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PairingSocketGroup != env["PAIRTASK_PAIRING_SOCKET_GROUP"] || cfg.PairingSocketGID != os.Getegid() {
		t.Fatal("configured group was lost")
	}
	want := env["PAIRTASK_PAIRING_EGRESS"][len("socket:"):]
	if cfg.PairingEgressSocket != want {
		t.Fatalf("egress socket %q, want %q", cfg.PairingEgressSocket, want)
	}
	if pc := pairingConfig(cfg); pc.Dial == nil || pc.Exchanger == nil {
		t.Fatal("socket route not wired")
	}
	assertRouteDisabledByDefault(t)
}

func assertRouteDisabledByDefault(t *testing.T) {
	t.Helper()
	disabled, err := loadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if disabled.PairingSocketGID != -1 || disabled.PairingEgressSocket != "" {
		t.Fatal("shared route enabled by default")
	}
}

func TestPairingRouteAcceptanceInvalidChoicesFailClosed(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"group without endpoint", "PAIRTASK_PAIRING_SOCKET", ""},
		{"unknown group", "PAIRTASK_PAIRING_SOCKET_GROUP", "pairtask-group-that-does-not-exist-178"},
		{"empty egress path", "PAIRTASK_PAIRING_EGRESS", "socket:"},
		{"relative egress path", "PAIRTASK_PAIRING_EGRESS", "socket:relative.sock"},
		{"unknown egress scheme", "PAIRTASK_PAIRING_EGRESS", "tcp:host:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := routeBrokerEnv(t)
			env[tc.key] = tc.value
			if _, err := readRouteConfig(env); err == nil {
				t.Fatal("invalid route configuration accepted")
			}
		})
	}
}

func TestPairingRouteAcceptanceProtectedDirectoriesCannotBeShared(t *testing.T) {
	for _, endpoint := range []string{"PAIRTASK_PAIRING_SOCKET", "PAIRTASK_PAIRING_EGRESS"} {
		for _, protected := range []string{"agent", "operator", "db", "identity"} {
			t.Run(endpoint+"/"+protected, func(t *testing.T) { assertProtectedRoute(t, endpoint, protected, false) })
			t.Run(endpoint+"/"+protected+"-alias", func(t *testing.T) { assertProtectedRoute(t, endpoint, protected, true) })
		}
	}
}

func assertProtectedRoute(t *testing.T, endpoint, protected string, alias bool) {
	t.Helper()
	env := routeBrokerEnv(t)
	dirs := map[string]string{
		"agent":    filepath.Dir(env["PAIRTASK_SOCKET_PATH"]),
		"operator": filepath.Dir(env["PAIRTASK_OPERATOR_SOCKET_PATH"]),
		"db":       filepath.Dir(env["PAIRTASK_DB_PATH"]),
		"identity": env["PAIRTASK_DB_PATH"] + ".peer",
	}
	dir := dirs[protected]
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0750); err != nil {
		t.Fatal(err)
	}
	if alias {
		link := filepath.Join(filepath.Dir(dirs["db"]), "alias")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		dir = link
	}
	path := filepath.Join(dir, "nested", "peer.sock")
	if endpoint == "PAIRTASK_PAIRING_EGRESS" {
		path = "socket:" + path
	}
	env[endpoint] = path
	if _, err := readRouteConfig(env); err == nil {
		t.Fatal("shared route exposed a protected directory")
	}
}

func TestPairingRouteAcceptanceRelativeProtectedPathsStayProtected(t *testing.T) {
	for _, key := range []string{"PAIRTASK_SOCKET_PATH", "PAIRTASK_OPERATOR_SOCKET_PATH", "PAIRTASK_DB_PATH"} {
		t.Run(key, func(t *testing.T) {
			env := routeBrokerEnv(t)
			absolute := env[key]
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			env[key], err = filepath.Rel(cwd, absolute)
			if err != nil {
				t.Fatal(err)
			}
			env["PAIRTASK_PAIRING_SOCKET"] = filepath.Join(filepath.Dir(absolute), "shared.sock")
			if _, err := readRouteConfig(env); err == nil {
				t.Fatal("relative protected path bypassed directory separation")
			}
		})
	}
}

func TestPairingRouteAcceptanceSharedAncestorCannotContainPrivateState(t *testing.T) {
	env := routeBrokerEnv(t)
	root := filepath.Dir(filepath.Dir(env["PAIRTASK_DB_PATH"]))
	env["PAIRTASK_PAIRING_SOCKET"] = filepath.Join(root, "shared.sock")
	if _, err := readRouteConfig(env); err == nil {
		t.Fatal("shared root containing private state was accepted")
	}
}

func TestPairingRouteAcceptanceDatabaseFileSymlinkTargetStaysPrivate(t *testing.T) {
	env := routeBrokerEnv(t)
	target := filepath.Join(filepath.Dir(env["PAIRTASK_PAIRING_SOCKET"]), "actual.db")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, env["PAIRTASK_DB_PATH"]); err != nil {
		t.Fatal(err)
	}
	if _, err := readRouteConfig(env); err == nil {
		t.Fatal("database symlink exposed its target through the shared directory")
	}
}
