package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDSHBotBrowser(t *testing.T) {
	if _, err := exec.LookPath("dsh"); err != nil {
		t.Skip("DSH browser test requires the official dsh executable on PATH")
	}
	runSystemBrowser(t, "dshbot_browser.test.mjs")
}

func startNativeDebugFixture(t *testing.T, origin string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "dshbot")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../dshbot")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native debug service: %v\n%s", err, out)
	}
	socket := filepath.Join(dir, "web.sock")
	t.Setenv("OMNIRELAY_DSHBOT_SOCKET", socket)
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, number, _ := net.SplitHostPort(port.Addr().String())
	port.Close()
	cmd := exec.Command(binary, "-home", filepath.Join(dir, "home"), "-socket", socket, "-origin", origin+"/", "-port", number)
	agents := filepath.Join(dir, "agents")
	agent := filepath.Join(agents, strings.Repeat("a", 64))
	workspace := filepath.Join(agent, "workspace")
	project := "--" + strings.Trim(strings.ReplaceAll(workspace, "/", "-"), "-") + "--"
	sessionDir := filepath.Join(agent, "home", "sessions", project, "fixture-project-session")
	for _, folder := range []string{workspace, sessionDir} {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
	}
	header, err := json.Marshal(map[string]any{"type": "session", "version": 4, "id": "fixture-project-session", "cwd": workspace, "createdAt": 1, "isSeeded": false, "delegationDepth": 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.v4.jsonl"), append(header, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "model-key")
	if err := os.WriteFile(key, []byte("fixture-key"), 0600); err != nil {
		t.Fatal(err)
	}
	plugins, err := filepath.Abs("../../runtime/dsh")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "OMNI_AGENT_DIRECTORY="+agents, "OMNI_CONSOLE_PLUGINS="+plugins,
		"DEEPSEEK_API_KEY_FILE="+key, "DSH_MODEL=deepseek-v4-flash", "DEEPSEEK_BASE_URL=http://127.0.0.1:1")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait() })
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 2 * time.Second}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		resp, err := client.Get("http://dshbot/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("native DSH Web did not become ready")
}
