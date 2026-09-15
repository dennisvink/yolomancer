package agentapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Runner func(context.Context, Work, func(Event) error) error
type Server struct {
	cfg     ServerConfig
	agents  map[string]Registered
	profile string
	runner  Runner
	slots   chan struct{}
	mu      sync.Mutex
	runs    map[string]context.CancelFunc
}

func NewServer(cfg ServerConfig, agents map[string]Registered, profile string, runner Runner) *Server {
	if runner == nil {
		runner = ProcessRunner(cfg.BearerTokenEnv)
	}
	return &Server{cfg: cfg, agents: agents, profile: profile, runner: runner, slots: make(chan struct{}, cfg.MaxConcurrent), runs: map[string]context.CancelFunc{}}
}
func (s *Server) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.runs {
		cancel()
	}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// No CORS and no browser-origin requests: prevent localhost drive-by API use.
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		problem(w, 403, "browser_origin_denied")
		return
	}
	if s.cfg.BearerToken != "" {
		want, got := sha256.Sum256([]byte("Bearer "+s.cfg.BearerToken)), sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			problem(w, 401, "unauthorized")
			return
		}
	} else {
		// Reject DNS-rebinding hosts even when the socket is loopback-only.
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			problem(w, 403, "invalid_host")
			return
		}
	}
	switch {
	case r.URL.Path == "/healthz" && r.Method == "GET":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	case r.URL.Path == "/v1/agents" && r.Method == "GET":
		names := make([]string, 0, len(s.agents))
		for name := range s.agents {
			names = append(names, name)
		}
		sort.Strings(names)
		list := []any{}
		for _, name := range names {
			a := s.agents[name]
			ts := []string{}
			for t := range a.Allowed {
				ts = append(ts, t)
			}
			sort.Strings(ts)
			list = append(list, map[string]any{"name": name, "tools": ts, "permission_mode": a.Agent.Permissions.Mode, "timeout_seconds": a.Agent.Limits.TimeoutSeconds})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"agents": list})
	case r.URL.Path == "/v1/runs" && r.Method == "POST":
		s.run(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/runs/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
		s.mu.Lock()
		cancel := s.runs[id]
		s.mu.Unlock()
		if cancel == nil {
			problem(w, 404, "run_not_active")
			return
		}
		cancel()
		w.WriteHeader(http.StatusAccepted)
	default:
		problem(w, 404, "not_found")
	}
}
func problem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code})
}
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil {
		problem(w, 400, "invalid_request")
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF || strings.TrimSpace(body.Prompt) == "" || len(body.Prompt) > 32768 {
		problem(w, 400, "invalid_request")
		return
	}
	a, ok := s.agents[body.Agent]
	if !ok {
		problem(w, 404, "unknown_agent")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		problem(w, 429, "concurrency_limit")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(a.Agent.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	id := uuid.NewString()
	s.mu.Lock()
	s.runs[id] = cancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.runs, id); s.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	seq := 0
	terminal := false
	emit := func(e Event) error {
		seq++
		encoded, err := json.Marshal(map[string]any{"type": e.Type, "run_id": id, "sequence": seq, "data": e.Data})
		if err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, e.Type, encoded); err != nil {
			return err
		}
		return controller.Flush()
	}
	if emit(Event{"run.started", map[string]string{"agent": body.Agent}}) != nil {
		return
	}
	events := make(chan Event)
	finished := make(chan error, 1)
	go func() {
		finished <- s.runner(ctx, Work{Registered: a, Prompt: body.Prompt, Profile: s.profile}, func(e Event) error {
			select {
			case events <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case e := <-events:
			if terminal {
				continue
			}
			if strings.HasPrefix(e.Type, "run.") && e.Type != "run.started" {
				terminal = true
			}
			if emit(e) != nil {
				cancel()
				<-finished
				return
			}
		case err := <-finished:
			if !terminal {
				if ctx.Err() != nil {
					kind, code := "run.cancelled", "cancelled"
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						kind, code = "run.failed", "deadline_exceeded"
					}
					_ = emit(Event{kind, map[string]string{"code": code}})
					return
				}
				code := "worker_error"
				if err == nil {
					code = "missing_completion"
				}
				_ = emit(Event{"run.failed", map[string]string{"code": code}})
			}
			return
		case <-ctx.Done():
			if !terminal {
				kind, code := "run.cancelled", "cancelled"
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					kind, code = "run.failed", "deadline_exceeded"
				}
				_ = emit(Event{kind, map[string]string{"code": code}})
			}
			<-finished
			return
		case <-ticker.C:
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				cancel()
				<-finished
				return
			}
			if controller.Flush() != nil {
				cancel()
				<-finished
				return
			}
		}
	}
}

// ProcessRunner isolates cwd, model config, child processes and conversation.
// It is not an OS security sandbox: yolo agents are trusted code.
func ProcessRunner(tokenEnv string) Runner {
	return func(ctx context.Context, work Work, emit func(Event) error) error {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		input, err := json.Marshal(work)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, binary, "internal-api-worker")
		cmd.Dir = work.Registered.Agent.Workspace
		cmd.Stdin = strings.NewReader(string(input))
		for _, value := range os.Environ() {
			key, _, _ := strings.Cut(value, "=")
			if key != tokenEnv && key != "YOLOMANCER_API_TOKEN" {
				cmd.Env = append(cmd.Env, value)
			}
		}
		// Worker errors are structured events; never echo arbitrary stderr/secrets.
		cmd.Stderr = io.Discard
		configureProcess(cmd)
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		defer cleanupProcess(cmd)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 65536), 2<<20)
		for scanner.Scan() {
			var event Event
			if json.Unmarshal(scanner.Bytes(), &event) != nil || strings.ContainsAny(event.Type, "\r\n") {
				cleanupProcess(cmd)
				_ = cmd.Wait()
				return fmt.Errorf("invalid worker event")
			}
			if err := emit(event); err != nil {
				cleanupProcess(cmd)
				_ = cmd.Wait()
				return err
			}
		}
		err = scanner.Err()
		if err != nil {
			cleanupProcess(cmd)
		}
		waitErr := cmd.Wait()
		if err != nil {
			return err
		}
		return waitErr
	}
}
