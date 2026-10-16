package gateway

import (
	"bufio"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

// openaiProvider behaves the way a real OpenAI-compatible provider does, which
// is the part the earlier mock got wrong: it refuses any path but its own.
//
// A stand-in that answers everything hides exactly the bug this file exists to
// fix -- routing that swapped the credential header and nothing else, so a
// request arrived at /openai/v1/messages and Groq said it had never heard of
// that URL.
func openaiProvider(t *testing.T, roots *x509.CertPool) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":{"message":"Unknown request URL: %s %s"}}`, r.Method, r.URL.Path)
			return
		}
		var in map[string]any
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "not json", http.StatusBadRequest)
			return
		}

		// A prompt asking for it gets the answer cut short by a content filter,
		// the way OpenAI-compatible providers report one.
		finish := "stop"
		if strings.Contains(string(body), "trip the filter") {
			finish = "content_filter"
		}

		if in["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			f := w.(http.Flusher)
			for _, word := range []string{"I ", "am ", "a ", "model."} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", word)
				f.Flush()
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\n", finish)
			fmt.Fprint(w, "data: [DONE]\n\n")
			f.Flush()
			return
		}

		// Echo back what arrived, so a test can inspect the translated request.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1","model":%q,"choices":[{"message":{"content":%q},"finish_reason":%q}],"usage":{"prompt_tokens":7,"completion_tokens":4}}`,
			in["model"], "I am a model.", finish)
	}))
	t.Cleanup(srv.Close)
	roots.AddCert(srv.Certificate())
	return srv.URL
}

func translatingGateway(t *testing.T) (*httptest.Server, *mint.Mint) {
	t.Helper()
	roots := x509.NewCertPool()
	upstream := openaiProvider(t, roots)

	routes, err := NewRoutes([]Route{{
		Model: "llama-3.1-8b-instant", Style: StyleOpenAI,
		Upstream: upstream, Credential: "gsk-pooled", CredentialEnv: "GROQ_API_KEY",
	}})
	if err != nil {
		t.Fatalf("NewRoutes: %v", err)
	}
	m, err := mint.New(mintKey(t), mint.OpenAuthorizer{})
	if err != nil {
		t.Fatalf("mint.New: %v", err)
	}
	gw, err := New(Config{
		Addr: "127.0.0.1:0", Routes: routes,
		MintKeys: map[string]*rsa.PublicKey{m.KeyID(): m.PublicKey()},
		Spent:    mint.NewSpentSet(), Budget: UnlimitedBudget{}, UpstreamRootCAs: roots, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	front := httptest.NewServer(gw.Handler())
	t.Cleanup(front.Close)
	return front, m
}

func tokenFor(t *testing.T, m *mint.Mint) string {
	t.Helper()
	bl, _ := mint.Blind(m.PublicKey())
	sig, err := m.Issue(context.Background(), nil, bl.Blinded)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	tok, err := bl.Unblind(sig)
	if err != nil {
		t.Fatalf("Unblind: %v", err)
	}
	return tok.Encode()
}

// The bug as reported: a client asking in the Messages API reached a provider
// that speaks OpenAI's, and got "Unknown request URL: POST /openai/v1/messages"
// back instead of an answer.
func TestAnthropicRequestReachesAnOpenAIProvider(t *testing.T) {
	front, m := translatingGateway(t)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(
		`{"model":"llama-3.1-8b-instant","max_tokens":64,"messages":[{"role":"user","content":"who are you"}]}`))
	req.Header.Set(TokenHeader, tokenFor(t, m))
	req.Header.Set("Content-Type", "application/json")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "Unknown request URL") {
		t.Fatalf("the request went to the wrong path: %s", body)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	// And it comes back in the shape the client asked in.
	var out struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("answer is not JSON: %v\n%s", err, body)
	}
	if out.Type != "message" || len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Fatalf("answer is not in the Messages shape: %s", body)
	}
	if out.Content[0].Text != "I am a model." {
		t.Fatalf("text = %q", out.Content[0].Text)
	}
	if out.Usage.OutputTokens != 4 {
		t.Fatalf("usage was not carried across: %s", body)
	}
}

// Streaming has to survive the conversion, and arrive as it is produced. A
// translation that buffered the whole answer would turn token streaming into
// one long pause.
func TestStreamingSurvivesTranslation(t *testing.T) {
	front, m := translatingGateway(t)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(
		`{"model":"llama-3.1-8b-instant","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(TokenHeader, tokenFor(t, m))
	req.Header.Set("Content-Type", "application/json")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want an event stream", ct)
	}

	var text strings.Builder
	var types []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &ev) != nil {
			continue
		}
		types = append(types, ev.Type)
		if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" {
			text.WriteString(ev.Delta.Text)
		}
	}

	if got := text.String(); got != "I am a model." {
		t.Fatalf("reassembled %q, want the whole answer", got)
	}
	// The client's parser keys off these, so their presence is the contract.
	for _, want := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_stop"} {
		found := false
		for _, got := range types {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the stream never sent %s; saw %v", want, types)
		}
	}
}

// --------------------------------------------------------------------------
// the conversion itself
// --------------------------------------------------------------------------

// A system prompt sits beside the messages in one API and inside them in the
// other. Dropping it would quietly change how the model behaves, which is the
// worst kind of translation bug because nothing errors.
func TestTheSystemPromptSurvives(t *testing.T) {
	_, out, err := toOpenAI([]byte(`{"model":"m","system":"You are terse.","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("toOpenAI: %v", err)
	}
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("got %d messages, want the system prompt plus the user turn: %s", len(got.Messages), out)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "You are terse." {
		t.Fatalf("system prompt = %+v", got.Messages[0])
	}
}

func TestTheRequestPathIsRewritten(t *testing.T) {
	path, _, err := toOpenAI([]byte(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("toOpenAI: %v", err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("path = %q; sending the Messages path is what produced \"Unknown request URL\"", path)
	}
}

// Anthropic allows content as a list of blocks; OpenAI wants a string.
func TestTextBlocksAreFlattened(t *testing.T) {
	_, out, err := toOpenAI([]byte(
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"one "},{"type":"text","text":"two"}]}]}`))
	if err != nil {
		t.Fatalf("toOpenAI: %v", err)
	}
	if !strings.Contains(string(out), `"content":"one two"`) {
		t.Fatalf("blocks were not flattened: %s", out)
	}
}

// Anything that is not plain text passes through rather than being silently
// reduced to nothing. It will fail at the provider, which is the right
// outcome: a dropped image is worse than a refused one.
func TestNonTextContentIsNotSilentlyDropped(t *testing.T) {
	_, out, err := toOpenAI([]byte(
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"data":"AAAA"}}]}]}`))
	if err != nil {
		t.Fatalf("toOpenAI: %v", err)
	}
	if !strings.Contains(string(out), "image") {
		t.Fatalf("the image was dropped instead of passed on: %s", out)
	}
}

func TestSamplingParametersAreCarried(t *testing.T) {
	_, out, err := toOpenAI([]byte(
		`{"model":"m","max_tokens":99,"temperature":0.25,"stop_sequences":["END"],"messages":[]}`))
	if err != nil {
		t.Fatalf("toOpenAI: %v", err)
	}
	for _, want := range []string{`"max_tokens":99`, `"temperature":0.25`, `"stop":["END"]`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("%s missing from %s", want, out)
		}
	}
}

// A provider's own error is more use to whoever reads it than a translation
// of one, so errors pass through untouched.
func TestProviderErrorsAreNotTranslated(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid API Key"}}`)),
	}
	if err := translateBack(resp); err != nil {
		t.Fatalf("translateBack: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Invalid API Key") {
		t.Fatalf("the provider's error was lost: %s", body)
	}
}

func TestStreamTranslationDoesNotStall(t *testing.T) {
	// A slow provider must not be buffered into a single lump at the end.
	pr, pw := io.Pipe()
	go func() {
		fmt.Fprint(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		time.Sleep(2 * time.Second)
		fmt.Fprint(pw, "data: {\"choices\":[{\"delta\":{\"content\":\" second\"}}]}\n\n")
		pw.Close()
	}()

	out := streamFromOpenAI(pr)
	defer out.Close()

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		var seen strings.Builder
		for {
			n, err := out.Read(buf)
			seen.Write(buf[:n])
			if strings.Contains(seen.String(), "first") || err != nil {
				done <- seen.String()
				return
			}
		}
	}()

	select {
	case got := <-done:
		if !strings.Contains(got, "first") {
			t.Fatalf("read %q without the first token", got)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the first token had not arrived after 1.5s; the stream is being buffered")
	}
}

func TestStopReasonSpeaksTheMessagesVocabulary(t *testing.T) {
	for finish, want := range map[string]string{
		"stop":           "end_turn",
		"":               "end_turn",
		"length":         "max_tokens",
		"content_filter": "refusal",
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		// Unknown reasons are passed on, not turned into an ordinary end.
		"provider_specific_reason": "provider_specific_reason",
	} {
		if got := stopReason(finish); got != want {
			t.Errorf("stopReason(%q) = %q, want %q", finish, got, want)
		}
	}
}

// A provider's content filter must reach a Messages-API client as the stop
// reason that API uses for it, both whole and streamed.
func TestAFilteredAnswerArrivesAsARefusal(t *testing.T) {
	front, m := translatingGateway(t)
	ask := func(stream bool) string {
		t.Helper()
		body := `{"model":"llama-3.1-8b-instant","max_tokens":64,"messages":[{"role":"user","content":"trip the filter"}]}`
		if stream {
			body = strings.Replace(body, `"max_tokens":64,`, `"max_tokens":64,"stream":true,`, 1)
		}
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set(TokenHeader, tokenFor(t, m))
		req.Header.Set("Content-Type", "application/json")
		resp, err := front.Client().Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, raw)
		}
		return string(raw)
	}

	var whole struct {
		StopReason string `json:"stop_reason"`
	}
	if raw := ask(false); json.Unmarshal([]byte(raw), &whole) != nil || whole.StopReason != "refusal" {
		t.Fatalf("non-streamed stop_reason = %q in %s", whole.StopReason, raw)
	}

	var streamed string
	for _, line := range strings.Split(ask(true), "\n") {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data:")), &ev) == nil && ev.Type == "message_delta" {
			streamed = ev.Delta.StopReason
		}
	}
	if streamed != "refusal" {
		t.Fatalf("streamed stop_reason = %q, want refusal", streamed)
	}
}

// output_tokens on the closing message_delta should be the provider's count
// when the stream carries one. Counting content chunks is only an estimate:
// one chunk can hold several tokens, and OpenAI puts its own count in a final
// chunk with no choices, which an earlier loop skipped outright.
func TestStreamReportsTheProvidersOwnTokenCount(t *testing.T) {
	const chunks = "data: {\"choices\":[{\"delta\":{\"content\":\"one two\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" three\"},\"finish_reason\":\"stop\"}]}\n\n"

	for _, tc := range []struct {
		name, tail  string
		want, input int // input -1: no input_tokens field at all
	}{
		{"openai usage chunk", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":17}}\n\ndata: [DONE]\n\n", 17, 9},
		{"groq x_groq usage", "data: {\"choices\":[],\"x_groq\":{\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":17}}}\n\ndata: [DONE]\n\n", 17, 9},
		{"no usage at all", "data: [DONE]\n\n", 2, -1},
		{"zero usage is no report", "data: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n", 2, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := streamFromOpenAI(io.NopCloser(strings.NewReader(chunks + tc.tail)))
			defer out.Close()
			body, err := io.ReadAll(out)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}

			got, input, text := -1, -1, ""
			sc := bufio.NewScanner(strings.NewReader(string(body)))
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				var ev struct {
					Type  string `json:"type"`
					Delta struct {
						Text string `json:"text"`
					} `json:"delta"`
					Usage struct {
						OutputTokens int  `json:"output_tokens"`
						InputTokens  *int `json:"input_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &ev) != nil {
					continue
				}
				switch ev.Type {
				case "content_block_delta":
					text += ev.Delta.Text
				case "message_delta":
					got = ev.Usage.OutputTokens
					if ev.Usage.InputTokens != nil {
						input = *ev.Usage.InputTokens
					}
				}
			}
			if text != "one two three" {
				t.Fatalf("text = %q; the usage chunk disturbed the content", text)
			}
			if got != tc.want {
				t.Fatalf("output_tokens = %d, want %d\n%s", got, tc.want, body)
			}
			// Unreported is absent, never a claimed zero.
			if input != tc.input {
				t.Fatalf("input_tokens = %d, want %d (-1 meaning absent)\n%s", input, tc.input, body)
			}
		})
	}
}

// A provider that fails partway through a stream says so in a chunk of its
// own. The translation must not then close the message as if it had ended
// normally: a client would show half an answer as the whole of one.
func TestStreamCarriesAMidStreamProviderError(t *testing.T) {
	for _, tc := range []struct {
		name, chunk, want string
	}{
		{"with a message", `{"error":{"message":"upstream overloaded","type":"server_error"}}`, "upstream overloaded"},
		{"without one", `{"error":{"code":500}}`, "partway through the response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := "data: {\"choices\":[{\"delta\":{\"content\":\"half an\"}}]}\n\n" +
				"data: " + tc.chunk + "\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\" answer\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n"
			out := streamFromOpenAI(io.NopCloser(strings.NewReader(stream)))
			defer out.Close()
			body, err := io.ReadAll(out)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			got := string(body)

			if !strings.Contains(got, "half an") {
				t.Fatalf("the text before the error was lost:\n%s", got)
			}
			var sawError bool
			for _, line := range strings.Split(got, "\n") {
				payload, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var ev struct {
					Type  string `json:"type"`
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal([]byte(payload), &ev) != nil {
					t.Fatalf("emitted invalid JSON %q", payload)
				}
				switch ev.Type {
				case "error":
					sawError = true
					if ev.Error.Type != "api_error" || !strings.Contains(ev.Error.Message, tc.want) {
						t.Fatalf("error event = %+v, want an api_error mentioning %q", ev.Error, tc.want)
					}
				case "message_delta", "message_stop":
					t.Fatalf("a failed stream was closed as complete with %s:\n%s", ev.Type, got)
				}
			}
			if !sawError {
				t.Fatalf("no error event:\n%s", got)
			}
			if strings.Contains(got, " answer") {
				t.Fatalf("text after the error was still forwarded:\n%s", got)
			}
		})
	}
}

// A 200 with no choices is not an answer. Some providers report failures that
// way, and translating one produced an empty end_turn message: a client saw a
// successful, silent reply. The provider's own body is passed on instead.
func TestAResponseWithNoChoicesIsNotTranslatedIntoAnEmptyAnswer(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"model overloaded, retry later","type":"server_error"}}`,
		`{"id":"chatcmpl-1","choices":[]}`,
		`{}`,
	} {
		if out, err := fromOpenAI([]byte(body)); err == nil {
			t.Fatalf("fromOpenAI(%s) = %s, want a refusal", body, out)
		}

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		if err := translateBack(resp); err != nil {
			t.Fatalf("translateBack: %v", err)
		}
		got, _ := io.ReadAll(resp.Body)
		if string(got) != body {
			t.Fatalf("client received %s, want the provider's body %s unchanged", got, body)
		}
	}
}

// A response too large to hold for translation is an error, not a shorter
// response. Cut at the limit, it failed to parse and was then handed to the
// client whole-looking, as a 200 carrying truncated JSON.
func TestAnOversizedTranslatedResponseIsAnErrorNotATruncation(t *testing.T) {
	answer := `{"choices":[{"message":{"content":"` + strings.Repeat("a", MaxRoutedBody) + `"},"finish_reason":"stop"}]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(answer)),
	}
	if err := translateBack(resp); err == nil || !strings.Contains(err.Error(), "exceeds") {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("translateBack = %v, passing on %d of %d bytes; want a size error", err, len(got), len(answer))
	}

	// At the limit exactly it still translates.
	fits := `{"choices":[{"message":{"content":"` + strings.Repeat("a", MaxRoutedBody-64) + `"}}]}`
	resp.Body = io.NopCloser(strings.NewReader(fits))
	if err := translateBack(resp); err != nil {
		t.Fatalf("a response within the limit was refused: %v", err)
	}
}
