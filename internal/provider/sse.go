package provider

import "strings"

type SSEEvent struct{ Event, Data string }
type SSEParser struct {
	pending, event string
	data           []string
}

func (p *SSEParser) Push(chunk string) []SSEEvent {
	p.pending += chunk
	var out []SSEEvent
	for {
		idx := strings.IndexByte(p.pending, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(p.pending[:idx], "\r")
		p.pending = p.pending[idx+1:]
		if line == "" {
			if len(p.data) > 0 {
				out = append(out, SSEEvent{p.event, strings.Join(p.data, "\n")})
			}
			p.event = ""
			p.data = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if ok {
			val = strings.TrimPrefix(val, " ")
		}
		switch key {
		case "event":
			p.event = val
		case "data":
			p.data = append(p.data, val)
		}
	}
	return out
}
func (p *SSEParser) Finish() []SSEEvent {
	if p.pending != "" {
		p.pending += "\n"
	}
	out := p.Push("")
	if len(p.data) > 0 {
		out = append(out, SSEEvent{p.event, strings.Join(p.data, "\n")})
		p.data = nil
	}
	return out
}
