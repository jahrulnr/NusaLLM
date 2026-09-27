package websocket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	httpdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/http"
	wsdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket"
	wsconversation "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textcompletion "github.com/jahrulnr/nusallm/infrastructure/service/core/text/completion"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// stubSender answers sends instantly.
type stubSender struct{}

func (stubSender) Send(_ context.Context, convID, _, _, _ string,
	_ []valueobject.Attachment, _ event.TurnEventHandler) (appconversation.SendResult, error) {
	return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
}

func (stubSender) Edit(context.Context, string, string, string, event.TurnEventHandler) error {
	return nil
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
}

func TestUpgradeHostRejected(t *testing.T) {
	// Any Host other than the canonical authority fails closed at the
	// global guard — before upgrade machinery runs.
	hub := wsconversation.NewHub(t.Context(), stubSender{})
	defer hub.Shutdown()
	srv := httptest.NewServer(httpdelivery.NewHandler(httpdelivery.Deps{
		Authority: "127.0.0.1:1", // deliberately not the listener's addr
		WS:        wsdelivery.NewHandler(hub, "127.0.0.1:1").Serve,
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/ws", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content-type %q", ct)
	}
}

func TestUpgradeOriginRejected(t *testing.T) {
	hub := wsconversation.NewHub(t.Context(), stubSender{})
	defer hub.Shutdown()
	srv, authority := wsServer(t, hub, httpdelivery.Deps{})
	defer srv.Close()

	for _, tc := range []struct {
		name   string
		origin string
	}{
		{"cross-site origin", "http://evil.example"},
		{"other port", "http://127.0.0.1:9"},
		{"scheme mismatch", "https://" + authority},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Origin", tc.origin)
			_, resp, err := websocket.DefaultDialer.Dial(wsURL(srv), h)
			if err == nil {
				t.Fatal("dial should have been rejected")
			}
			if resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %v, want 403", resp)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Fatalf("content-type %q", ct)
			}
		})
	}
}

func TestUpgradeSuccessExactOrigin(t *testing.T) {
	hub := wsconversation.NewHub(t.Context(), stubSender{})
	defer hub.Shutdown()
	srv, authority := wsServer(t, hub, httpdelivery.Deps{})
	defer srv.Close()

	h := http.Header{}
	h.Set("Origin", "http://"+authority)
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL(srv), h)
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer ws.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := ws.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"turn.send","requestId":"r1","payload":{"content":"hi"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Type string `json:"type"`
		Seq  *int   `json:"seq"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Type != "turn.opened" || f.Seq == nil || *f.Seq != 0 {
		t.Fatalf("first frame %s — want turn.opened seq 0", raw)
	}
}

// wsServer mounts the full router (guard + upgrade edge + hub) on a
// loopback listener and returns the server plus its authority.
func wsServer(t *testing.T, hub *wsconversation.Hub, deps httpdelivery.Deps) (*httptest.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	authority := ln.Addr().String()
	deps.Authority = authority
	deps.WS = wsdelivery.NewHandler(hub, authority).Serve
	srv := httptest.NewUnstartedServer(httpdelivery.NewHandler(deps))
	srv.Listener = ln
	srv.Start()
	return srv, authority
}

// Integration: a real ConversationApp on temp repos + a stub completion
// upstream — the WS turn must persist and be readable back over HTTP.
func TestTurnSendEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer upstream.Close()

	gw, err := aggregate.NewGateway("gw1", "stub", upstream.URL, enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	gw.DisableStream()
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()
	gateways := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	app := appconversation.New(
		convs,
		gateways,
		map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{})},
		nil,
		config.NewConfig(),
		"",
	)
	defer func() { _ = app.Shutdown(context.Background()) }()

	hub := wsconversation.NewHub(t.Context(), app)
	defer hub.Shutdown()
	srv, authority := wsServer(t, hub, httpdelivery.Deps{Conversations: app})
	defer srv.Close()

	h := http.Header{}
	h.Set("Origin", "http://"+authority)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv), h)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"turn.send","requestId":"r1","payload":{"route":"gw1","modelId":"m1","content":"ping"}}`)); err != nil {
		t.Fatal(err)
	}
	var convID, turnID string
	for {
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, raw, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var f struct {
			Type           string `json:"type"`
			ConversationID string `json:"conversationId"`
			TurnID         string `json:"turnId"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		if f.Type == "turn.failed" || f.Type == "error" {
			t.Fatalf("send failed: %s", raw)
		}
		if f.Type == "turn.completed" {
			convID, turnID = f.ConversationID, f.TurnID
			break
		}
	}
	if !strings.HasPrefix(convID, "c_") || !strings.HasPrefix(turnID, "t_") {
		t.Fatalf("ids: conv=%q turn=%q", convID, turnID)
	}

	// The turn committed: the HTTP history read serves it back.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/conversations/"+convID+"/messages", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		t.Fatalf("history status %d: %s", resp.StatusCode, buf[:n])
	}
	var body struct {
		Messages []struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "user" || body.Messages[0].Content != "ping" ||
		body.Messages[1].Role != "assistant" || body.Messages[1].Content != "pong" {
		t.Fatalf("history: %+v", body.Messages)
	}

	// turn.edit rewrites the user message end-to-end: the regenerated
	// turn completes, and history reflects the rewrite.
	msgID := body.Messages[0].ID
	if msgID == "" {
		t.Fatal("user message id missing from history projection")
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(
		`{"type":"turn.edit","requestId":"e1","conversationId":%q,"payload":{"messageId":%q,"content":"second"}}`,
		convID, msgID))); err != nil {
		t.Fatal(err)
	}
	for {
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, raw, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var f struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		if f.Type == "turn.failed" || f.Type == "error" {
			t.Fatalf("edit failed: %s", raw)
		}
		if f.Type == "turn.completed" {
			// Edit terminals never claim userMessagePersisted.
			if strings.Contains(string(f.Payload), "userMessagePersisted") {
				t.Fatalf("edit terminal claims persistence: %s", f.Payload)
			}
			break
		}
	}
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/conversations/"+convID+"/messages", nil)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var body2 struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body2); err != nil {
		t.Fatal(err)
	}
	if len(body2.Messages) != 2 || body2.Messages[0].Content != "second" ||
		body2.Messages[1].Role != "assistant" {
		t.Fatalf("post-edit history: %+v", body2.Messages)
	}
}
