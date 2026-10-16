package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Clients speak one API. Providers do not.
//
// A gateway that only swapped the credential header would be routing in name
// only: an Anthropic-shaped request sent to an OpenAI-shaped provider arrives
// at the wrong path, carrying the wrong body, and comes back in a shape the
// client cannot read. That is the whole reason a client would rather hold one
// endpoint than five, so the translation belongs here.
//
// The direction is fixed: clients speak Anthropic's Messages API, because that
// is what this network's own client and interface speak. Providers speaking
// that format are passed through untouched; providers speaking OpenAI's are
// translated in both directions, including mid-stream.
//
// # What is not translated
//
// Tool use, images, extended thinking and structured output all differ between
// the two APIs in ways a field rename does not cover. The paid gateway policy
// rejects those before this translator runs; silently passing or dropping them
// would make both behavior and provider cost ambiguous.

// anthropicRequest is the subset of the Messages API worth translating.
type anthropicRequest struct {
	Model         string            `json:"model"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Messages      []json.RawMessage `json:"messages"`
	System        json.RawMessage   `json:"system,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// toOpenAI rewrites an Anthropic request body for an OpenAI-style provider,
// and reports the path it should be sent to.
func toOpenAI(body []byte) (path string, out []byte, err error) {
	var in anthropicRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return "", nil, fmt.Errorf("this request is not valid JSON: %w", err)
	}

	msgs := make([]chatMessage, 0, len(in.Messages)+1)

	// Anthropic carries the system prompt beside the messages; OpenAI carries
	// it as the first message. Dropping it would quietly change the model's
	// behaviour, which is the worst kind of translation bug.
	if len(in.System) > 0 {
		content := in.System
		// A system prompt may be a bare string or a list of text blocks.
		if flat, ok := flattenContent(in.System); ok {
			content, _ = json.Marshal(flat)
		}
		msgs = append(msgs, chatMessage{Role: "system", Content: content})
	}

	for _, raw := range in.Messages {
		var m chatMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", nil, fmt.Errorf("a message in this request could not be read: %w", err)
		}
		if flat, ok := flattenContent(m.Content); ok {
			m.Content, _ = json.Marshal(flat)
		}
		msgs = append(msgs, m)
	}

	req := map[string]any{"model": in.Model, "messages": msgs}
	if in.MaxTokens > 0 {
		req["max_tokens"] = in.MaxTokens
	}
	if in.Stream {
		req["stream"] = true
	}
	if in.Temperature != nil {
		req["temperature"] = *in.Temperature
	}
	if in.TopP != nil {
		req["top_p"] = *in.TopP
	}
	if len(in.StopSequences) > 0 {
		req["stop"] = in.StopSequences
	}

	out, err = json.Marshal(req)
	if err != nil {
		return "", nil, err
	}
	return "/v1/chat/completions", out, nil
}

// flattenContent turns Anthropic's list of content blocks into the plain
// string OpenAI expects, when every block is text.
//
// It reports false for anything else -- an image, a tool result -- so those
// pass through untouched and fail visibly at the provider instead of being
// silently reduced to nothing.
func flattenContent(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return "", false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", false
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type != "text" {
			return "", false
		}
		b.WriteString(blk.Text)
	}
	return b.String(), true
}

// --------------------------------------------------------------------------
// responses
// --------------------------------------------------------------------------

// chatCompletion is the subset of an OpenAI answer worth translating.
type chatCompletion struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// fromOpenAI rewrites a completed OpenAI answer as an Anthropic one.
func fromOpenAI(body []byte) ([]byte, error) {
	var in chatCompletion
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	text, stop := "", "end_turn"
	if len(in.Choices) > 0 {
		text = in.Choices[0].Message.Content
		stop = stopReason(in.Choices[0].FinishReason)
	}
	return json.Marshal(map[string]any{
		"id":          orDefault(in.ID, "msg_osanwe"),
		"type":        "message",
		"role":        "assistant",
		"model":       in.Model,
		"content":     []map[string]string{{"type": "text", "text": text}},
		"stop_reason": stop,
		"usage": map[string]int{
			"input_tokens":  in.Usage.PromptTokens,
			"output_tokens": in.Usage.CompletionTokens,
		},
	})
}

// stopReason maps an OpenAI finish_reason onto the Messages API's stop_reason.
// Reasons with an equivalent are translated, because a client written against
// the Messages API branches on its own vocabulary and would not recognise
// OpenAI's. Anything else passes through unchanged rather than being disguised
// as an ordinary end of turn.
func stopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "stop", "":
		return "end_turn"
	case "content_filter":
		// The provider's filter cut the answer short; "refusal" is the Messages
		// API's word for a response stopped on policy grounds.
		return "refusal"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return finish
	}
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// --------------------------------------------------------------------------
// streams
// --------------------------------------------------------------------------

// streamFromOpenAI rewrites an OpenAI event stream as an Anthropic one, as it
// arrives.
//
// Buffering the whole stream to translate it would defeat the point of
// streaming, so this reads events, converts each, and writes the result
// through a pipe. The client sees words appear at the same rate the provider
// produced them.
func streamFromOpenAI(src io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()

	go func() {
		defer src.Close()
		var err error
		defer func() { pw.CloseWithError(err) }()

		w := bufio.NewWriter(pw)
		emit := func(event string, payload any) bool {
			body, mErr := json.Marshal(payload)
			if mErr != nil {
				err = mErr
				return false
			}
			if _, wErr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); wErr != nil {
				err = wErr
				return false
			}
			// Flushed per event, or the buffering here would reintroduce
			// exactly the delay this function exists to avoid.
			return w.Flush() == nil
		}

		if !emit("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_osanwe", "type": "message", "role": "assistant",
				"content": []any{}, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
			},
		}) {
			return
		}
		if !emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]string{"type": "text", "text": ""},
		}) {
			return
		}

		sc := bufio.NewScanner(src)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		// produced counts content chunks. It stands in for output_tokens only
		// when the provider reports no usage of its own; reported holds the
		// provider's count when the stream carries one.
		stop, produced, reported := "end_turn", 0, 0

		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			type usage struct {
				CompletionTokens int `json:"completion_tokens"`
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				// OpenAI sends usage in a final chunk with no choices; Groq
				// sends it under x_groq in its last chunk.
				Usage *usage `json:"usage"`
				XGroq struct {
					Usage *usage `json:"usage"`
				} `json:"x_groq"`
				// Some providers report a failure inside a stream that has
				// already started, as a chunk carrying an error object.
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(payload), &chunk) != nil {
				continue
			}
			if chunk.Error != nil {
				// The answer so far is incomplete. Ending it with message_stop
				// and an end_turn stop reason would pass it off as whole, so
				// send the Messages API's own error event and stop there.
				message := chunk.Error.Message
				if message == "" {
					message = "the provider reported an error partway through the response"
				}
				emit("error", map[string]any{
					"type":  "error",
					"error": map[string]string{"type": "api_error", "message": message},
				})
				return
			}
			for _, u := range []*usage{chunk.Usage, chunk.XGroq.Usage} {
				if u != nil && u.CompletionTokens > 0 {
					reported = u.CompletionTokens
				}
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			if r := chunk.Choices[0].FinishReason; r != "" {
				stop = stopReason(r)
			}
			text := chunk.Choices[0].Delta.Content
			if text == "" {
				continue
			}
			produced++
			if !emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]string{"type": "text_delta", "text": text},
			}) {
				return
			}
		}
		if scErr := sc.Err(); scErr != nil {
			err = scErr
			return
		}

		if !emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}) {
			return
		}
		outputTokens := produced
		if reported > 0 {
			outputTokens = reported
		}
		if !emit("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stop},
			"usage": map[string]int{"output_tokens": outputTokens},
		}) {
			return
		}
		emit("message_stop", map[string]any{"type": "message_stop"})
	}()

	return pr
}
