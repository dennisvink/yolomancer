//go:build js && wasm

// Package ws adapts browser WebSockets to net.Conn without eval or blocking
// JavaScript callbacks. All frames sent are binary, matching the SSH relay.
package ws

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"syscall/js"
	"time"
)

type address string

func (a address) Network() string { return "websocket" }
func (a address) String() string  { return string(a) }

type connection struct {
	socket    js.Value
	callbacks []js.Func
	mu        sync.Mutex
	buffer    bytes.Buffer
	err       error
	wake      chan struct{}
	done      chan struct{}
	once      sync.Once
	addr      address
}

func Dial(url string) (_ net.Conn, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("cannot create browser WebSocket")
		}
	}()
	c := &connection{socket: js.Global().Get("WebSocket").New(url), wake: make(chan struct{}, 1), done: make(chan struct{}), addr: address(url)}
	c.socket.Set("binaryType", "arraybuffer")
	opened := make(chan struct{}, 1)
	bind := func(name string, handler func(js.Value, []js.Value) any) {
		f := js.FuncOf(handler)
		c.callbacks = append(c.callbacks, f)
		c.socket.Set(name, f)
	}
	bind("onopen", func(js.Value, []js.Value) any {
		select {
		case opened <- struct{}{}:
		default:
		}
		return nil
	})
	bind("onerror", func(js.Value, []js.Value) any { c.fail(errors.New("WebSocket connection failed")); return nil })
	bind("onclose", func(js.Value, []js.Value) any { c.fail(io.EOF); return nil })
	bind("onmessage", func(_ js.Value, args []js.Value) any {
		value := args[0].Get("data")
		var data []byte
		if value.Type() == js.TypeString {
			data = []byte(value.String())
		} else {
			array := js.Global().Get("Uint8Array").New(value)
			data = make([]byte, array.Get("byteLength").Int())
			js.CopyBytesToGo(data, array)
		}
		c.mu.Lock()
		overflow := c.buffer.Len()+len(data) > 8*1024*1024
		if !overflow {
			c.buffer.Write(data)
		}
		c.mu.Unlock()
		if overflow {
			c.fail(errors.New("WebSocket receive buffer exceeded"))
		} else {
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
		return nil
	})
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-opened:
		return c, nil
	case <-c.done:
		return nil, c.err
	case <-timer.C:
		c.Close()
		return nil, errors.New("WebSocket connection timeout")
	}
}

func (c *connection) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		for _, name := range []string{"onopen", "onerror", "onclose", "onmessage"} {
			c.socket.Set(name, js.Null())
		}
		c.socket.Call("close")
		for _, callback := range c.callbacks {
			callback.Release()
		}
		close(c.done)
	})
}
func (c *connection) Close() error { c.fail(net.ErrClosed); return nil }
func (c *connection) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		c.mu.Lock()
		if c.buffer.Len() > 0 {
			n, e := c.buffer.Read(p)
			c.mu.Unlock()
			return n, e
		}
		err := c.err
		c.mu.Unlock()
		if err != nil {
			return 0, err
		}
		select {
		case <-c.wake:
		case <-c.done:
		}
	}
}
func (c *connection) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	err = c.err
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	defer func() {
		if recover() != nil {
			n = 0
			err = errors.New("WebSocket send failed")
			c.fail(err)
		}
	}()
	if c.socket.Get("bufferedAmount").Int()+len(p) > 8*1024*1024 {
		err = errors.New("WebSocket send buffer exceeded")
		c.fail(err)
		return 0, err
	}
	array := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(array, p)
	c.socket.Call("send", array)
	return len(p), nil
}
func (c *connection) LocalAddr() net.Addr  { return address("browser") }
func (c *connection) RemoteAddr() net.Addr { return c.addr }
func (c *connection) SetDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	return errors.New("WebSocket deadlines unsupported")
}
func (c *connection) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *connection) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }
