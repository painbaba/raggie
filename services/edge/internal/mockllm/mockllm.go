// SPDX-License-Identifier: Apache-2.0

// Package mockllm is a tiny, deterministic OpenAI-compatible chat server for
// CI end-to-end tests and fully offline demos. It never calls out: answers are
// templated from the request, citing the first one or two numbered context
// sources ([1], [2]) the edge places in the prompt, so retrieval/citation
// plumbing can be exercised without a real model.
package mockllm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config tunes the mock. The zero value is usable (no delay, default models).
type Config struct {
	Addr       string        // listen address (MOCKLLM_ADDR)
	TokenDelay time.Duration // pause between streamed tokens (MOCKLLM_TOKEN_DELAY_MS)
	Models     []string      // ids served by GET /v1/models (MOCKLLM_MODELS)
	ToolCalls  bool          // answer with a tool call when tools are offered (MOCKLLM_TOOL_CALLS)
	Reasoning  bool          // stream a short reasoning delta first (MOCKLLM_REASONING)
	APIKey     string        // when set, require "Authorization: Bearer <key>" (MOCKLLM_API_KEY)
	Language   string        // answer template language, "en" or "tr" (MOCKLLM_LANGUAGE)
}

// DefaultModels covers the edge's default chat model so readiness passes.
var DefaultModels = []string{"gpt-oss-120b", "raggie-mock"}

// LoadConfig reads the MOCKLLM_* environment variables.
func LoadConfig() Config {
	cfg := Config{
		Addr:      envOr("MOCKLLM_ADDR", ":11435"),
		ToolCalls: envBool("MOCKLLM_TOOL_CALLS"),
		Reasoning: envBool("MOCKLLM_REASONING"),
		APIKey:    os.Getenv("MOCKLLM_API_KEY"),
		Language:  envLanguage("MOCKLLM_LANGUAGE"),
	}
	if ms, err := strconv.Atoi(os.Getenv("MOCKLLM_TOKEN_DELAY_MS")); err == nil && ms > 0 {
		cfg.TokenDelay = time.Duration(ms) * time.Millisecond
	}
	for _, m := range strings.Split(os.Getenv("MOCKLLM_MODELS"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			cfg.Models = append(cfg.Models, m)
		}
	}
	if len(cfg.Models) == 0 {
		cfg.Models = DefaultModels
	}
	return cfg
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	b, _ := strconv.ParseBool(os.Getenv(key))
	return b
}

// envLanguage reads an answer-template language knob. Only "en" and "tr" are
// supported (case-insensitive); anything else, including empty, means English.
func envLanguage(key string) string {
	return normalizeLanguage(os.Getenv(key))
}

func normalizeLanguage(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "tr") {
		return "tr"
	}
	return "en"
}

// Server serves the OpenAI-compatible endpoints.
type Server struct {
	cfg Config
}

func New(cfg Config) *Server {
	if len(cfg.Models) == 0 {
		cfg.Models = DefaultModels
	}
	cfg.Language = normalizeLanguage(cfg.Language)
	return &Server{cfg: cfg}
}

// Handler returns the HTTP routes: GET /healthz, GET /v1/models and
// POST /v1/chat/completions (stream true/false).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /v1/models", s.auth(http.HandlerFunc(s.models)))
	mux.Handle("POST /v1/chat/completions", s.auth(http.HandlerFunc(s.chat)))
	return mux
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey != "" && r.Header.Get("Authorization") != "Bearer "+s.cfg.APIKey {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]string{"message": "invalid api key", "type": "invalid_request_error"},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, len(s.cfg.Models))
	for _, id := range s.cfg.Models {
		data = append(data, map[string]any{"id": id, "object": "model", "owned_by": "raggie-mock"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

type chatRequest struct {
	Model    string           `json:"model"`
	Stream   bool             `json:"stream"`
	Messages []map[string]any `json:"messages"`
	Tools    []struct {
		Function struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil || len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid request", "type": "invalid_request_error"},
		})
		return
	}
	model := req.Model
	if model == "" {
		model = s.cfg.Models[0]
	}
	promptTokens := 0
	for _, m := range req.Messages {
		promptTokens += len(strings.Fields(contentText(m["content"])))
	}

	var call *toolCall
	if s.cfg.ToolCalls && len(req.Tools) > 0 && !answeredByTool(req.Messages) {
		call = buildToolCall(req.Tools[0].Function.Name, req.Tools[0].Function.Parameters, lastUserText(req.Messages))
	}
	answer := ""
	if call == nil {
		answer = answerText(req.Messages, s.cfg.Language)
	}
	tokens := tokenize(answer)
	id := "chatcmpl-mock"
	created := time.Now().Unix()

	if !req.Stream {
		msg := map[string]any{"role": "assistant", "content": answer}
		finish := "stop"
		if call != nil {
			msg = map[string]any{"role": "assistant", "content": nil, "tool_calls": []toolCall{*call}}
			finish = "tool_calls"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   usage(promptTokens, max(len(tokens), 1)),
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	chunk := func(delta map[string]any, finish any, extra map[string]any) bool {
		ev := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		for k, v := range extra {
			ev[k] = v
		}
		b, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return r.Context().Err() == nil
	}
	pause := func() bool {
		if s.cfg.TokenDelay <= 0 {
			return true
		}
		select {
		case <-r.Context().Done():
			return false
		case <-time.After(s.cfg.TokenDelay):
			return true
		}
	}

	if call != nil {
		if !chunk(map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
			"index": 0, "id": call.ID, "type": "function",
			"function": map[string]string{"name": call.Function.Name, "arguments": call.Function.Arguments},
		}}}, nil, nil) {
			return
		}
		chunk(map[string]any{}, "tool_calls", map[string]any{"usage": usage(promptTokens, 1)})
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if s.cfg.Reasoning {
		if !chunk(map[string]any{"role": "assistant", "reasoning": reasoningFor(req.Messages)}, nil, nil) || !pause() {
			return
		}
	}
	for i, tok := range tokens {
		if i > 0 && !pause() {
			return
		}
		if !chunk(map[string]any{"content": tok}, nil, nil) {
			return
		}
	}
	chunk(map[string]any{}, "stop", map[string]any{"usage": usage(promptTokens, len(tokens))})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func usage(prompt, completion int) map[string]int {
	return map[string]int{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}
}

// tokenize splits on spaces, keeping each space attached, so the streamed
// deltas concatenate back to exactly the answer.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, " ")
}

// ── Deterministic answers ─────────────────────────────────────────────────

// sourcePattern matches the edge's numbered passage labels: documents
// "[1] (source: file.pdf, relevance: 0.91)", web evidence
// "[1] (source: Title - https://…)" or "(source: Title — https://…)", and the
// Turkish labels older edge builds used ("kaynak", "alaka", "tür").
var sourcePattern = regexp.MustCompile(
	`(?m)^\[(\d+)\] \((?:source|kaynak): (.*?)(?:, (?:relevance|alaka):|, (?:type|tür):| - | — |\))`)

// answerTemplates holds the user-visible template prose per answer language
// (MOCKLLM_LANGUAGE=en|tr). Only this prose switches: citation labels stay
// "[n]" in every language, and the shape-correct helper replies the edge
// parses ("NONE", the schedule JSON) stay byte-identical so offline runs keep
// working regardless of the language.
var answerTemplates = map[string]struct {
	preamble   string
	toolResult string
	question   string
	noSource   string
	source     string
	moreDetail string
}{
	"en": {
		preamble:   "This is a Raggie mock answer (offline test model).",
		toolResult: " Tool result: ",
		question:   " Your question: “%s”.",
		noSource:   " The provided context contains no matching source for this question.",
		source:     " The relevant information is in %s [%s].",
		moreDetail: " For more detail, see %s [%s].",
	},
	"tr": {
		preamble:   "Bu bir Raggie örnek yanıtıdır (çevrimdışı test modeli).",
		toolResult: " Araç sonucu: ",
		question:   " Sorunuz: “%s”.",
		noSource:   " Verilen bağlamda bu soruyla eşleşen bir kaynak bulunamadı.",
		source:     " İlgili bilgi %s [%s] içinde yer alıyor.",
		moreDetail: " Ayrıntı için bkz. %s [%s].",
	},
}

// Answer returns the deterministic reply for a conversation in English, the
// default template language:
//   - after a tool result: a summary quoting the tool output;
//   - for the edge's helper prompts (memory extraction, schedule parse): a
//     shape-correct reply so offline runs neither store junk memory nor fail;
//   - otherwise a templated answer citing up to two provided sources.
func Answer(messages []map[string]any) string {
	return answerText(messages, "en")
}

// answerText is Answer with the template language chosen by
// MOCKLLM_LANGUAGE ("en" or "tr"; anything else falls back to English).
func answerText(messages []map[string]any, lang string) string {
	tpl := answerTemplates[normalizeLanguage(lang)]
	if res, ok := lastToolResult(messages); ok {
		return tpl.preamble + tpl.toolResult + clip(res, 300)
	}
	system := ""
	for _, m := range messages {
		if role, _ := m["role"].(string); role == "system" {
			system += contentText(m["content"]) + "\n"
		}
	}
	question := clip(strings.Join(strings.Fields(lastUserText(messages)), " "), 200)
	switch {
	case strings.Contains(system, "NONE yaz") || strings.Contains(system, "only NONE"):
		return "NONE"
	case strings.Contains(system, `"cron"`):
		b, _ := json.Marshal(map[string]string{
			"cron": "0 9 * * *", "prompt": question, "name": "Mock task", "summary": "Every day at 09:00",
		})
		return string(b)
	}

	sources := citedSources(messages, 2)
	var b strings.Builder
	b.WriteString(tpl.preamble)
	fmt.Fprintf(&b, tpl.question, question)
	if len(sources) == 0 {
		b.WriteString(tpl.noSource)
		return b.String()
	}
	fmt.Fprintf(&b, tpl.source, sources[0].name, sources[0].n)
	if len(sources) > 1 {
		fmt.Fprintf(&b, tpl.moreDetail, sources[1].name, sources[1].n)
	}
	return b.String()
}

type source struct{ n, name string }

func citedSources(messages []map[string]any, limit int) []source {
	var out []source
	seen := map[string]bool{}
	for _, m := range messages {
		for _, match := range sourcePattern.FindAllStringSubmatch(contentText(m["content"]), -1) {
			if seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			out = append(out, source{n: match[1], name: strings.TrimSpace(match[2])})
			if len(out) == limit {
				return out
			}
		}
	}
	return out
}

func reasoningFor(messages []map[string]any) string {
	return fmt.Sprintf("Mock reasoning: reviewed %d sources.", len(citedSources(messages, 100)))
}

// contentText flattens an OpenAI message content (string, null or parts).
func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if part, ok := p.(map[string]any); ok {
				if t, _ := part["text"].(string); t != "" {
					b.WriteString(t)
					b.WriteString("\n")
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func lastUserText(messages []map[string]any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if role, _ := messages[i]["role"].(string); role == "user" {
			return contentText(messages[i]["content"])
		}
	}
	return ""
}

// lastToolResult returns the newest tool message that follows the last user turn.
func lastToolResult(messages []map[string]any) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		switch role, _ := messages[i]["role"].(string); role {
		case "tool":
			return contentText(messages[i]["content"]), true
		case "user":
			return "", false
		}
	}
	return "", false
}

func answeredByTool(messages []map[string]any) bool {
	_, ok := lastToolResult(messages)
	return ok
}

// buildToolCall fills every required string parameter with the user's question.
func buildToolCall(name string, schema json.RawMessage, question string) *toolCall {
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(schema, &s)
	args := map[string]string{}
	for _, req := range s.Required {
		if p, ok := s.Properties[req]; ok && p.Type == "string" {
			args[req] = strings.TrimSpace(question)
		}
	}
	b, _ := json.Marshal(args)
	c := &toolCall{ID: "call_mock_1", Type: "function"}
	c.Function.Name = name
	c.Function.Arguments = string(b)
	return c
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Probe GETs url and succeeds on 200: the container healthcheck
// (`mockllm -healthcheck`), since the distroless image has no curl.
func Probe(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}
