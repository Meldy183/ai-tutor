package app

import (
	"context"
	"encoding/json"
	voicepb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/voicegateway/v1"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/testkit"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"google.golang.org/grpc"

	llmgatewayv1 "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/llmgateway/v1"
)

// Exercise production model adapters through HTTP and gRPC, with
// deterministic responses owned by the tests rather than an offline
// application mode.
func configureFixtureModels(t *testing.T, cfg *Config) *testkit.Fixture {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/transcribe":
			_ = json.NewEncoder(w).Encode(map[string]string{"text": "Ответ ученика."})
		case "/synthesize":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(wavBytes())
		default:
			t.Errorf("unexpected model path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	state := testkit.New(t, provider.URL+"/transcribe", provider.URL+"/synthesize", cfg.VoiceProfile)
	if strings.Contains(cfg.VoiceGatewayAddress, ".test") {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer(grpc.MaxRecvMsgSize(26<<20), grpc.MaxSendMsgSize(65<<20))
		voicepb.RegisterVoiceGatewayServer(server, state)
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
		cfg.VoiceGatewayAddress = listener.Addr().String()
	}
	if strings.Contains(cfg.LLMGatewayAddress, ".test") {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer()
		llmgatewayv1.RegisterGatewayServer(server, fixtureGateway{})
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
		cfg.LLMGatewayAddress = listener.Addr().String()
	}
	return state
}

// fixtureGateway answers Geek prompts deterministically: a question prompt
// gets a question on the topic; an evaluation grades each student answer by
// length ("не знаю" is don't know, under 4 words weak, under 12 partial,
// otherwise full), K1 and K2 the best over the answers, K3 and K4 the latest.
type fixtureGateway struct {
	llmgatewayv1.UnimplementedGatewayServer
}

var (
	studentLine = regexp.MustCompile(`(?m)^\[(\d+)\] Студент: (.*)$`)
	topicLine   = regexp.MustCompile(`(?m)^Тема: (.*)$`)
)

func (fixtureGateway) Complete(_ context.Context, r *llmgatewayv1.CompleteRequest) (*llmgatewayv1.CompleteResponse, error) {
	prompt := r.GetMessages()[len(r.GetMessages())-1].GetContent()
	var answer any
	if strings.Contains(r.GetMessages()[0].GetContent(), `"name"`) && strings.Contains(r.GetMessages()[0].GetContent(), `"has_answer"`) {
		answer = map[string]any{"name": "", "has_answer": true}
	} else if strings.Contains(r.GetMessages()[0].GetContent(), "Проверь, есть ли в реплике кроме представления") {
		answer = map[string]any{"has_answer": true}
	} else if strings.Contains(r.GetMessages()[0].GetContent(), "Сократи названия узлов учебного графа") {
		var input struct {
			Titles []string `json:"titles"`
		}
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			return nil, err
		}
		labels := make([]string, len(input.Titles))
		for i := range labels {
			labels[i] = "Короткая тема"
		}
		answer = map[string]any{"labels": labels}
	} else if strings.Contains(r.GetMessages()[0].GetContent(), "абзац общего вывода") {
		answer = map[string]string{"summary": "Общая картина и главный следующий шаг."}
	} else if !strings.Contains(prompt, "Оцени ответ на заданный вопрос") {
		topic := topicLine.FindStringSubmatch(prompt)
		answer = map[string]string{"text": "Расскажите про тему «" + topic[1] + "»?"}
	} else {
		lines := studentLine.FindAllStringSubmatch(prompt, -1)
		last := lines[len(lines)-1][2]
		if strings.Contains(strings.ToLower(last), "не знаю") {
			answer = map[string]any{"verdict": "dont_know"}
		} else {
			k := [4]int{}
			evidence := []json.Number{}
			for _, line := range lines {
				if strings.Contains(strings.ToLower(line[2]), "не знаю") {
					continue
				}
				c := byLength(line[2])
				k[0], k[1], k[2], k[3] = max(k[0], c[0]), max(k[1], c[1]), c[2], c[3]
				evidence = append(evidence, json.Number(line[1]))
			}
			// The longer the answer, the more of the topic's scale it earns.
			maximum := 1
			if strings.Contains(prompt, "по шкале темы: 0, 1 или 2") {
				maximum = 2
			}
			score := 0
			switch {
			case k[0] == 2 && k[1] == 2:
				score = maximum
			case k[0] >= 1 && maximum == 2:
				score = 1
			}
			answer = map[string]any{"verdict": "answered", "score": score, "reasons": []string{"по длине ответа"}, "evidence_lines": evidence}
		}
	}
	content, _ := json.Marshal(answer)
	completion, _ := json.Marshal(map[string]any{
		"id": "fixture", "object": "chat.completion", "created": 1, "model": "fixture",
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
	})
	return &llmgatewayv1.CompleteResponse{RequestId: "fixture", CompletionJson: completion}, nil
}

func byLength(text string) [4]int {
	words := len(strings.FieldsFunc(text, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }))
	switch {
	case words < 4:
		return [4]int{0, 1, 0, 0}
	case words < 12:
		return [4]int{2, 2, 1, 1}
	}
	return [4]int{2, 2, 2, 2}
}
