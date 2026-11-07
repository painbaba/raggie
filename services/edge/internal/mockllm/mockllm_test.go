// SPDX-License-Identifier: Apache-2.0

package mockllm

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicangunduz/raggie/services/edge/internal/llm"
	"github.com/alicangunduz/raggie/services/edge/internal/ports"
)

// ragContext uses the edge's language-neutral passage labels.
const ragContext = "CONTEXT:\n[1] (source: leave-policy.pdf, relevance: 0.91)\nAnnual leave is 14 days.\n\n" +
	"[2] (source: handbook.docx, relevance: 0.80)\nLeave is requested in the HR portal.\n\n" +
	"[3] (source: other.txt, relevance: 0.40)\nUnrelated.\n\n"

func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(cfg).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func chatBody(t *testing.T, stream bool, msgs []map[string]any, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"model": "gpt-oss-120b", "stream": stream, "messages": msgs}
	for k, v := range extra {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ragMessages(question string) []map[string]any {
	return []map[string]any{
		{"role": "system", "content": "You are Raggie. Cite the passage you used, like [1].\n\n" + ragContext},
		{"role": "user", "content": question},
	}
}

func TestAnswerCitesFirstTwoSources(t *testing.T) {
	got := Answer(ragMessages("How many leave days?"))
	for _, want := range []string{"[1]", "[2]", "leave-policy.pdf", "handbook.docx", "How many leave days?", "mock answer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("answer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[3]") {
		t.Fatalf("answer should cite at most two sources:\n%s", got)
	}
	if Answer(ragMessages("How many leave days?")) != got {
		t.Fatal("answer must be deterministic")
	}
}

func TestAnswerWithoutContextSaysSo(t *testing.T) {
	got := Answer([]map[string]any{{"role": "user", "content": "merhaba"}})
	if strings.Contains(got, "[1]") || !strings.Contains(got, "no matching source") {
		t.Fatalf("no-context answer should not cite: %s", got)
	}
}

// MOCKLLM_LANGUAGE=tr switches the template prose to Turkish while citations
// keep their [n] labels and the first-two-sources rule.
func TestAnswerTurkishTemplate(t *testing.T) {
	got := answerText(ragMessages("İzin kaç gün?"), "tr")
	for _, want := range []string{"[1]", "[2]", "leave-policy.pdf", "handbook.docx", "İzin kaç gün?", "örnek yanıtı"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Turkish answer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "mock answer") || strings.Contains(got, "[3]") {
		t.Fatalf("Turkish answer must not use the English template or cite [3]:\n%s", got)
	}
	if answerText(ragMessages("İzin kaç gün?"), "tr") != got {
		t.Fatal("Turkish answer must be deterministic")
	}
}

func TestAnswerTurkishWithoutContextSaysSo(t *testing.T) {
	got := answerText([]map[string]any{{"role": "user", "content": "merhaba"}}, "tr")
	if strings.Contains(got, "[1]") || !strings.Contains(got, "kaynak bulunamadı") {
		t.Fatalf("Turkish no-context answer should not cite: %s", got)
	}
}

// Tool-result summaries switch language too; the quoted output does not.
func TestAnswerTurkishToolResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "jira'da açık işler"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
			"id": "c1", "type": "function", "function": map[string]any{"name": "jira_search", "arguments": "{}"},
		}}},
		{"role": "tool", "tool_call_id": "c1", "content": "ISSUE-1: Fix login"},
	}
	got := answerText(msgs, "tr")
	if !strings.Contains(got, "Araç sonucu") || !strings.Contains(got, "ISSUE-1: Fix login") || strings.Contains(got, "Tool result") {
		t.Fatalf("Turkish tool-result summary wrong: %s", got)
	}
}

// Only the template prose changes: the shape-correct helper replies the edge
// parses stay byte-identical in Turkish mode.
func TestTurkishKeepsHelperPromptReplies(t *testing.T) {
	none := answerText([]map[string]any{
		{"role": "system", "content": "... Çıkarılacak kalıcı bilgi yoksa yalnızca NONE yaz."},
		{"role": "user", "content": "KULLANICI: selam"},
	}, "tr")
	if none != "NONE" {
		t.Fatalf("memory extraction must still answer NONE, got %q", none)
	}
	sched := answerText([]map[string]any{
		{"role": "system", "content": `YALNIZCA geçerli bir JSON nesnesi döndür: {"cron":"...","prompt":"...","name":"...","summary":"..."}`},
		{"role": "user", "content": "her sabah rapor"},
	}, "tr")
	var parsed map[string]string
	if err := json.Unmarshal([]byte(sched), &parsed); err != nil || len(strings.Fields(parsed["cron"])) != 5 {
		t.Fatalf("schedule parse reply must stay JSON with a 5-field cron, got %q (%v)", sched, err)
	}
}

func TestLoadConfigLanguage(t *testing.T) {
	t.Setenv("MOCKLLM_LANGUAGE", "")
	if cfg := LoadConfig(); cfg.Language != "en" {
		t.Fatalf("unset language must default to en, got %q", cfg.Language)
	}
	for in, want := range map[string]string{"tr": "tr", "TR": "tr", " tr ": "tr", "fr": "en", "english": "en"} {
		t.Setenv("MOCKLLM_LANGUAGE", in)
		if cfg := LoadConfig(); cfg.Language != want {
			t.Fatalf("MOCKLLM_LANGUAGE=%q → %q, want %q", in, cfg.Language, want)
		}
	}
}

// The env knob reaches the served answers: a Turkish-configured server answers
// in Turkish; an unset or unknown language falls back to English.
func TestServerAnswersInConfiguredLanguage(t *testing.T) {
	for lang, want := range map[string]string{"tr": "örnek yanıtı", "fr": "mock answer", "": "mock answer"} {
		srv := newTestServer(t, Config{Language: lang})
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(chatBody(t, false, ragMessages("Leave?"), nil)))
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(body.Choices) != 1 || !strings.Contains(body.Choices[0].Message.Content, want) {
			t.Fatalf("lang %q: expected %q in the answer, got %+v", lang, want, body.Choices)
		}
	}
}

// One-shot helper prompts used by the edge get shape-correct replies so an
// offline demo does not pollute memory or fail JSON parsing.
// Older edge builds labeled passages in Turkish; the mock still cites them.
func TestAnswerCitesLegacyTurkishLabels(t *testing.T) {
	got := Answer([]map[string]any{
		{"role": "system", "content": "BAĞLAM:\n[1] (kaynak: izin.pdf, alaka: 0.9)\nx\n\n[2] (kaynak: Başlık - https://ex.test)\ny"},
		{"role": "user", "content": "izin?"},
	})
	if !strings.Contains(got, "izin.pdf") || !strings.Contains(got, "[1]") || !strings.Contains(got, "Başlık") || !strings.Contains(got, "[2]") {
		t.Fatalf("legacy labels not cited: %s", got)
	}
}

// Web evidence labels "[n] (source: Title - URL)" cite the title.
func TestAnswerCitesWebEvidence(t *testing.T) {
	got := Answer([]map[string]any{
		{"role": "system", "content": "WEB RESULTS:\n[1] (source: Go Release Notes - https://go.dev/doc)\nnotes"},
		{"role": "user", "content": "what changed?"},
	})
	if !strings.Contains(got, "Go Release Notes") || !strings.Contains(got, "[1]") || strings.Contains(got, "https://") {
		t.Fatalf("web evidence citation wrong: %s", got)
	}
}

func TestAnswerHandlesEdgeHelperPrompts(t *testing.T) {
	enMemory := Answer([]map[string]any{
		{"role": "system", "content": "... If there is no durable fact to extract, reply with only NONE."},
		{"role": "user", "content": "USER: hi"},
	})
	if enMemory != "NONE" {
		t.Fatalf("English memory extraction should answer NONE, got %q", enMemory)
	}
	memory := Answer([]map[string]any{
		{"role": "system", "content": "... Çıkarılacak kalıcı bilgi yoksa yalnızca NONE yaz."},
		{"role": "user", "content": "KULLANICI: selam"},
	})
	if memory != "NONE" {
		t.Fatalf("memory extraction should answer NONE, got %q", memory)
	}
	sched := Answer([]map[string]any{
		{"role": "system", "content": `YALNIZCA geçerli bir JSON nesnesi döndür: {"cron":"...","prompt":"...","name":"...","summary":"..."}`},
		{"role": "user", "content": "her sabah rapor"},
	})
	var parsed map[string]string
	if err := json.Unmarshal([]byte(sched), &parsed); err != nil || len(strings.Fields(parsed["cron"])) != 5 {
		t.Fatalf("schedule parse reply must be JSON with a 5-field cron, got %q (%v)", sched, err)
	}
}

func TestModelsEndpoint(t *testing.T) {
	srv := newTestServer(t, Config{Models: []string{"gpt-oss-120b", "raggie-mock"}})
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 2 || body.Data[0].ID != "gpt-oss-120b" {
		t.Fatalf("unexpected models body: %+v", body)
	}
}

func TestNonStreamingCompletion(t *testing.T) {
	srv := newTestServer(t, Config{})
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(chatBody(t, false, ragMessages("Leave?"), nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "chat.completion" || len(body.Choices) != 1 ||
		body.Choices[0].FinishReason != "stop" || !strings.Contains(body.Choices[0].Message.Content, "[1]") ||
		body.Usage.CompletionTokens == 0 {
		t.Fatalf("unexpected completion: %+v", body)
	}
}

// The real edge streaming client consumes the mock end to end.
func TestStreamingCompletionWithEdgeClient(t *testing.T) {
	srv := newTestServer(t, Config{Reasoning: true})
	client := llm.NewChatProvider(srv.URL+"/v1", "any-key", "gpt-oss-120b", 256, 4)
	stream, err := client.Stream(context.Background(), ports.ChatRequest{Messages: []ports.Message{
		{Role: ports.RoleSystem, Content: "You are Raggie.\n\n" + ragContext},
		{Role: ports.RoleUser, Content: "Leave?"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var answer, reasoning strings.Builder
	done := false
	for c := range stream {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		if c.Done {
			done = c.Usage != nil && c.Usage.CompletionTokens > 0
			break
		}
		answer.WriteString(c.Delta)
		reasoning.WriteString(c.Reasoning)
	}
	if !done || !strings.Contains(answer.String(), "[1]") || reasoning.Len() == 0 {
		t.Fatalf("stream incomplete: done=%v answer=%q reasoning=%q", done, answer.String(), reasoning.String())
	}
	if answer.String() != Answer(ragMessages("Leave?")) {
		t.Fatalf("streamed answer differs from the deterministic answer:\n%q\n%q", answer.String(), Answer(ragMessages("Leave?")))
	}
}

func TestTokenDelayPacesTheStream(t *testing.T) {
	srv := newTestServer(t, Config{TokenDelay: 15 * time.Millisecond})
	start := time.Now()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(chatBody(t, true, []map[string]any{{"role": "user", "content": "a b c d"}}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The pause sits between content tokens only (not before the final stop
	// chunk), so N content chunks take at least N-1 delays.
	tokens := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: {") && strings.Contains(sc.Text(), `"content":`) {
			tokens++
		}
	}
	if elapsed := time.Since(start); tokens < 3 || elapsed < time.Duration(tokens-1)*15*time.Millisecond {
		t.Fatalf("expected paced tokens: %d content chunks in %v", tokens, elapsed)
	}
}

func TestToolCallsOnlyWhenEnabledAndOffered(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{
		"name": "jira_search", "parameters": map[string]any{
			"type": "object", "properties": map[string]any{"jql": map[string]any{"type": "string"}}, "required": []string{"jql"},
		},
	}}}
	msgs := []map[string]any{{"role": "user", "content": "open issues"}}

	// Disabled: tools are ignored, a normal answer comes back.
	off := newTestServer(t, Config{})
	client := llm.NewChatProvider(off.URL+"/v1", "", "m", 64, 2)
	if done := drain(t, client, msgs, tools); done.FinishReason != "stop" || len(done.ToolCalls) != 0 {
		t.Fatalf("tool calls must be off by default: %+v", done)
	}

	on := newTestServer(t, Config{ToolCalls: true})
	client = llm.NewChatProvider(on.URL+"/v1", "", "m", 64, 2)
	done := drain(t, client, msgs, tools)
	if done.FinishReason != "tool_calls" || len(done.ToolCalls) != 1 || done.ToolCalls[0].Name != "jira_search" ||
		!strings.Contains(done.ToolCalls[0].Arguments, `"jql":"open issues"`) {
		t.Fatalf("expected one jira_search call with the question as jql: %+v", done)
	}

	// After a tool result is in the conversation, the model answers from it.
	followUp := append(msgs,
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
			"id": done.ToolCalls[0].ID, "type": "function",
			"function": map[string]any{"name": "jira_search", "arguments": done.ToolCalls[0].Arguments}}}},
		map[string]any{"role": "tool", "tool_call_id": done.ToolCalls[0].ID, "content": "ISSUE-1: Fix login"},
	)
	resp, err := http.Post(on.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(chatBody(t, false, followUp, map[string]any{"tools": tools})))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Choices) != 1 || body.Choices[0].FinishReason != "stop" ||
		!strings.Contains(body.Choices[0].Message.Content, "ISSUE-1: Fix login") {
		t.Fatalf("expected a final answer quoting the tool result: %+v", body)
	}
}

func drain(t *testing.T, client *llm.ChatProvider, msgs []map[string]any, tools []map[string]any) ports.ChatChunk {
	t.Helper()
	pm := make([]ports.Message, 0, len(msgs))
	for _, m := range msgs {
		pm = append(pm, ports.Message{Role: m["role"].(string), Content: m["content"].(string)})
	}
	var specs []ports.ToolSpec
	for _, tl := range tools {
		fn := tl["function"].(map[string]any)
		params, _ := json.Marshal(fn["parameters"])
		specs = append(specs, ports.ToolSpec{Name: fn["name"].(string), Parameters: params})
	}
	stream, err := client.Stream(context.Background(), ports.ChatRequest{Messages: pm, Tools: specs})
	if err != nil {
		t.Fatal(err)
	}
	for c := range stream {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		if c.Done {
			return c
		}
	}
	t.Fatal("stream ended without a terminal chunk")
	return ports.ChatChunk{}
}

func TestAPIKeyEnforcedWhenConfigured(t *testing.T) {
	srv := newTestServer(t, Config{APIKey: "secret-key"})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", resp.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer secret-key")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with key, got %d", resp.StatusCode)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("MOCKLLM_ADDR", "")
	t.Setenv("MOCKLLM_TOKEN_DELAY_MS", "7")
	t.Setenv("MOCKLLM_MODELS", " a , b ,")
	t.Setenv("MOCKLLM_TOOL_CALLS", "true")
	cfg := LoadConfig()
	if cfg.Addr != ":11435" || cfg.TokenDelay != 7*time.Millisecond || !cfg.ToolCalls ||
		len(cfg.Models) != 2 || cfg.Models[1] != "b" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, Config{})
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	if err := Probe(srv.URL + "/healthz"); err != nil {
		t.Fatalf("probe: %v", err)
	}
}
