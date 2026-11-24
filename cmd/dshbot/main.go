// Command dshbot serves the official DSH Web profile to OmniRelay over a private Unix socket.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	origin := flag.String("origin", "", "OmniRelay HTTPS origin")
	home := flag.String("home", "", "absolute path to the dedicated debug workspace")
	socket := flag.String("socket", "/run/omnirelay-dshbot/web.sock", "private Unix socket shared with the Go service")
	port := flag.Int("port", 3080, "DSH loopback port")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *origin, *home, *socket, *port); err != nil {
		log.Fatal(err)
	}
}

func run(parent context.Context, origin, home, socket string, port int) error {
	public, err := url.Parse(origin)
	if err != nil || public.Scheme != "https" || public.Hostname() == "" || public.User != nil ||
		(public.Path != "" && public.Path != "/") || public.RawPath != "" || public.RawQuery != "" ||
		public.ForceQuery || strings.Contains(origin, "#") || !filepath.IsAbs(home) ||
		!filepath.IsAbs(socket) || port < 1 || port > 65535 {
		return errors.New("an HTTPS origin, absolute workspace and socket paths, and a valid port are required")
	}
	public.Host = strings.ToLower(public.Host)
	if public.Port() == "443" {
		public.Host = strings.TrimSuffix(public.Host, ":443")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	// The directory is provisioned by the operator and writable only by the debug service.
	lock, err := os.OpenFile(socket+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("debug socket is already in use")
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("debug socket path is occupied by a non-socket file")
		}
		// The socket directory persists after a container is killed. Check for
		// an older live listener before removing the stale directory entry.
		conn, dialErr := net.DialTimeout("unix", socket, time.Second)
		if dialErr == nil {
			conn.Close()
			return errors.New("debug socket has an active listener")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return dialErr
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0660); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	public.Path = "/dshbot/"
	mount := public.String()
	cmd := exec.CommandContext(ctx, "dsh", "web", "--no-open", "--host", "127.0.0.1",
		"--port", strconv.Itoa(port), "--public-url", mount, "--trusted-host", public.Host)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "DSH_HOME=" + filepath.Join(home, ".dsh"), "PATH=" + os.Getenv("PATH")}
	logs, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	reader := bufio.NewReader(logs)
	token := ""
	for token == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			return errors.New("DSH exited before publishing its Web URL")
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "dsh web: "+mount+"?") {
			u, err := url.Parse(strings.TrimPrefix(line, "dsh web: "))
			if err == nil {
				token = u.Query().Get("token")
			}
		}
	}
	upstream := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	cookies := nativeLogin{upstream: upstream, authority: public.Host, token: token}
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(upstream)
		p.Out.Host = public.Host
	}, ModifyResponse: func(r *http.Response) error {
		r.Header.Del("Set-Cookie")
		return nil
	}}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := cookies.get(r.Context())
		if err != nil {
			http.Error(w, "DSH Web authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Header.Del("Cookie")
		r.AddCookie(cookie)
		proxy.ServeHTTP(w, r)
	})}
	go func() {
		_, _ = io.Copy(io.Discard, reader) // The native log can include launch URLs.
		cancel()
	}()
	go func() { <-ctx.Done(); _ = server.Close() }()
	log.Print("DSH Web ready on private socket")
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		if parent.Err() == nil {
			return errors.New("native DSH Web exited")
		}
		return nil
	}
	return err
}

type nativeLogin struct {
	mu               sync.Mutex
	upstream         *url.URL
	authority, token string
	cookie           *http.Cookie
	refresh          time.Time
}

func (l *nativeLogin) get(ctx context.Context) (*http.Cookie, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cookie != nil && time.Now().Before(l.refresh) {
		return l.cookie, nil
	}
	u := *l.upstream
	u.Path = "/"
	u.RawQuery = url.Values{"token": {l.token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Host = l.authority
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("DSH loopback unavailable")
	}
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if res.StatusCode == http.StatusSeeOther && strings.HasPrefix(c.Name, "dsh-auth-") && c.HttpOnly && c.MaxAge > 0 {
			l.cookie = c
			l.refresh = time.Now().Add(time.Duration(c.MaxAge) * time.Second / 2)
			return c, nil
		}
	}
	return nil, fmt.Errorf("DSH credential exchange failed (%d)", res.StatusCode)
}
