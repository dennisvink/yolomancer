package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

const (
	maxBuffer    = 1024 * 1024
	maxProcesses = 64
)

type Manager struct {
	next      atomic.Int32
	mu        sync.Mutex
	processes map[int32]*Process
}
type Process struct {
	ID               int32
	Command, Workdir string
	TTY              bool
	Started          time.Time
	mu               sync.Mutex
	last             time.Time
	cmd              *exec.Cmd
	writer           io.WriteCloser
	output           rolling
	exited           bool
	exitCode         *int
}
type Summary struct {
	ID                  int32
	Command, Workdir    string
	TTY                 bool
	RunningFor, IdleFor time.Duration
}

type rolling struct {
	b       []byte
	omitted int
}

func (r *rolling) Write(p []byte) (int, error) {
	n := len(p)
	r.b = append(r.b, p...)
	if len(r.b) > maxBuffer {
		keep := maxBuffer / 2
		r.omitted += len(r.b) - 2*keep
		r.b = append(append([]byte{}, r.b[:keep]...), r.b[len(r.b)-keep:]...)
	}
	return n, nil
}
func (r *rolling) Drain() ([]byte, int) {
	out := append([]byte{}, r.b...)
	omitted := r.omitted
	r.b = nil
	r.omitted = 0
	return out, omitted
}
func (r *rolling) Since(pos int) (string, int) {
	if pos < 0 || pos > len(r.b) {
		pos = 0
	}
	return string(r.b[pos:]), len(r.b)
}

func New() *Manager { m := &Manager{processes: map[int32]*Process{}}; m.next.Store(0); return m }

type ExecOptions struct {
	Env                                     []string
	Command, DisplayCommand, Workdir, Shell string
	Login, TTY                              bool
	Yield                                   time.Duration
	MaxTokens                               int
}

func (m *Manager) Exec(ctx context.Context, o ExecOptions) (string, error) {
	if o.Command == "" {
		return "", fmt.Errorf("missing required string argument: cmd")
	}
	if o.Workdir == "" {
		var e error
		o.Workdir, e = os.Getwd()
		if e != nil {
			return "", e
		}
	}
	if o.Shell == "" {
		o.Shell = os.Getenv("SHELL")
		if o.Shell == "" {
			o.Shell = "/bin/sh"
		}
	}
	if o.Yield <= 0 {
		o.Yield = time.Second
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 10000
	}
	args := []string{"-c", o.Command}
	if o.Login {
		args = []string{"-lc", o.Command}
	}
	cmd := exec.CommandContext(ctx, o.Shell, args...)
	cmd.Env = o.Env
	cmd.Dir = o.Workdir
	m.mu.Lock()
	for id, existing := range m.processes {
		existing.mu.Lock()
		exited := existing.exited
		existing.mu.Unlock()
		if exited {
			delete(m.processes, id)
		}
	}
	if len(m.processes) >= maxProcesses {
		m.mu.Unlock()
		return "", fmt.Errorf("too many background terminal sessions are still running")
	}
	m.mu.Unlock()
	id := m.next.Add(1)
	displayCommand := o.DisplayCommand
	if displayCommand == "" {
		displayCommand = o.Command
	}
	p := &Process{ID: id, Command: displayCommand, Workdir: o.Workdir, TTY: o.TTY, Started: time.Now(), last: time.Now(), cmd: cmd}
	if o.TTY {
		f, err := pty.Start(cmd)
		if err != nil {
			return "", err
		}
		p.writer = f
		go p.copyAndWait(f)
	} else {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return "", err
		}
		if err := cmd.Start(); err != nil {
			return "", err
		}
		go io.Copy(writer{p}, stdout)
		go io.Copy(writer{p}, stderr)
		go p.wait()
	}
	m.mu.Lock()
	m.processes[id] = p
	m.mu.Unlock()
	timer := time.NewTimer(o.Yield)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return p.result(o.MaxTokens, true), nil
		case <-time.After(10 * time.Millisecond):
			p.mu.Lock()
			done := p.exited
			p.mu.Unlock()
			if done {
				return p.result(o.MaxTokens, false), nil
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

type writer struct{ p *Process }

func (w writer) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.output.Write(b)
}
func (p *Process) copyAndWait(f *os.File) { _, _ = io.Copy(writer{p}, f); p.wait() }
func (p *Process) wait() {
	err := p.cmd.Wait()
	code := 0
	if err != nil {
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		} else {
			code = -1
		}
	}
	p.mu.Lock()
	p.exited = true
	p.exitCode = &code
	p.mu.Unlock()
}

func (p *Process) result(maxTokens int, running bool) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, omitted := p.output.Drain()
	original := len(raw)
	out := string(raw)
	if maxBytes := maxTokens * 4; maxBytes > 0 && len(out) > maxBytes {
		out = strings.ToValidUTF8(out[len(out)-maxBytes:], "�")
	}
	v := map[string]any{"ok": true, "output": out, "session_id": p.ID, "running": running, "tty": p.TTY, "command": p.Command, "resolved_workdir": p.Workdir, "original_byte_count": original, "omitted_byte_count": omitted, "chunk_id": uuid.NewString()}
	if p.exitCode != nil {
		v["exit_code"] = *p.exitCode
		v["ok"] = *p.exitCode == 0
		v["session_id"] = nil
		v["running"] = false
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (m *Manager) Write(ctx context.Context, id int32, chars string, yield time.Duration, maxTokens int) (string, error) {
	m.mu.Lock()
	p := m.processes[id]
	m.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("unknown exec session %d", id)
	}
	p.mu.Lock()
	w := p.writer
	done := p.exited
	p.last = time.Now()
	p.mu.Unlock()
	if done {
		return p.result(maxTokens, false), nil
	}
	if chars != "" && !p.TTY {
		return "", fmt.Errorf("stdin is closed for this session; rerun exec_command with tty=true to keep stdin open")
	}
	if chars != "" && w != nil {
		if _, err := io.WriteString(w, chars); err != nil {
			return "", err
		}
	}
	if yield <= 0 {
		if chars == "" {
			yield = 5 * time.Second
		} else {
			yield = 250 * time.Millisecond
		}
	}
	timer := time.NewTimer(yield)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return p.result(maxTokens, true), nil
		case <-time.After(10 * time.Millisecond):
			p.mu.Lock()
			done = p.exited
			p.mu.Unlock()
			if done {
				return p.result(maxTokens, false), nil
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (m *Manager) List() []Summary {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []Summary
	for id, p := range m.processes {
		p.mu.Lock()
		if p.exited {
			delete(m.processes, id)
		} else {
			out = append(out, Summary{p.ID, p.Command, p.Workdir, p.TTY, now.Sub(p.Started), now.Sub(p.last)})
		}
		p.mu.Unlock()
	}
	return out
}
func (m *Manager) Stop(id int32) bool {
	m.mu.Lock()
	p := m.processes[id]
	delete(m.processes, id)
	m.mu.Unlock()
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		_ = p.cmd.Process.Kill()
	}
	return true
}
func (m *Manager) StopAll() int {
	m.mu.Lock()
	processes := m.processes
	m.processes = map[int32]*Process{}
	m.mu.Unlock()
	for _, p := range processes {
		p.mu.Lock()
		if !p.exited {
			_ = p.cmd.Process.Kill()
		}
		p.mu.Unlock()
	}
	return len(processes)
}

func limit(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	half := n / 2
	var b bytes.Buffer
	b.WriteString(s[:half])
	b.WriteString("\n... output truncated ...\n")
	b.WriteString(s[len(s)-half:])
	return b.String()
}
