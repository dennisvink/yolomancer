package adkbridge

import (
	"context"
	"iter"

	"github.com/dennisvink/yolomancer/internal/goal"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// goalLLM overlays fresh goal context on each request, including after a tool
// mutation or compaction. It never rewrites the persisted conversation or splits
// an assistant tool-use message from its following tool-result message.
type goalLLM struct {
	adkmodel.LLM
	goals *goal.Manager
}

func (m *goalLLM) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	if err := m.goals.Err(); err != nil {
		return func(yield func(*adkmodel.LLMResponse, error) bool) { yield(nil, err) }
	}
	copyRequest := *req
	if text := m.goals.Context(); text != "" {
		copyRequest.Contents = append([]*genai.Content(nil), req.Contents...)
		n := len(copyRequest.Contents)
		if n > 0 && copyRequest.Contents[n-1].Role == genai.RoleUser {
			last := *copyRequest.Contents[n-1]
			last.Parts = append(append([]*genai.Part(nil), last.Parts...), &genai.Part{Text: text})
			copyRequest.Contents[n-1] = &last
		} else {
			copyRequest.Contents = append(copyRequest.Contents, genai.NewContentFromText(text, genai.RoleUser))
		}
	}
	return m.LLM.GenerateContent(ctx, &copyRequest, stream)
}
