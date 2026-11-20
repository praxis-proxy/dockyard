package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatRequestPayload(t *testing.T) {
	r := ChatRequest{
		Model:    "demo",
		Messages: User("hello"),
		Stream:   true,
		Extra:    map[string]any{"temperature": 0.7},
	}
	p := r.payload()
	if p["model"] != "demo" {
		t.Errorf("model = %v", p["model"])
	}
	if p["stream"] != true {
		t.Errorf("stream = %v, want true", p["stream"])
	}
	if p["temperature"] != 0.7 {
		t.Errorf("extra field not merged: %v", p["temperature"])
	}
	msgs, ok := p["messages"].([]map[string]any)
	if !ok || len(msgs) != 1 || msgs[0]["role"] != "user" || msgs[0]["content"] != "hello" {
		t.Errorf("messages = %v", p["messages"])
	}
}

func TestChatRequestPayloadOmitsStreamWhenFalse(t *testing.T) {
	p := ChatRequest{Model: "m", Messages: User("hi")}.payload()
	if _, present := p["stream"]; present {
		t.Error("stream should be omitted when false")
	}
}

func TestPostSendsJSONBodyAndHeaders(t *testing.T) {
	var gotAuth, gotContentType, gotPerReq, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotPerReq = r.Header.Get("X-Probe")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := New(srv.URL, WithBearer("tok"))
	resp, err := c.Post(context.Background(), "/v1/chat/completions",
		map[string]any{"model": "demo"}, Header("X-Probe", "p1"))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
	if resp.String() != `{"ok":true}` {
		t.Errorf("body = %q", resp.String())
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want 'Bearer tok'", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotPerReq != "p1" {
		t.Errorf("per-request header = %q, want p1", gotPerReq)
	}
	if gotBody != `{"model":"demo"}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestResponseJSON(t *testing.T) {
	r := &Response{Body: []byte(`{"n":3}`)}
	var v struct {
		N int `json:"n"`
	}
	if err := r.JSON(&v); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if v.N != 3 {
		t.Errorf("n = %d, want 3", v.N)
	}
}

func TestChatCompletionStreamRecv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, chunk := range []string{`{"i":1}`, `{"i":2}`} {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// Base at the server root so ChatCompletionStream's fixed "/v1/chat/completions" resolves.
	stream, err := New(srv.URL).ChatCompletionStream(context.Background(),
		ChatRequest{Model: "demo", Messages: User("hi")})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	got := [][]byte{}
	for {
		data, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got = append(got, data)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (excluding [DONE])", len(got))
	}
	var first struct {
		I int `json:"i"`
	}
	if err := json.Unmarshal(got[0], &first); err != nil || first.I != 1 {
		t.Errorf("first chunk = %s (err %v)", got[0], err)
	}
}

func TestChatCompletionStreamNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "nope")
	}))
	defer srv.Close()
	if _, err := New(srv.URL).ChatCompletionStream(context.Background(),
		ChatRequest{Model: "m", Messages: User("x")}); err == nil {
		t.Error("expected an error for a non-200 stream open")
	}
}
