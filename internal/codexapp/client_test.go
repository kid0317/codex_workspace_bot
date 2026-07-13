package codexapp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
)

func TestClientSendsInitializeBeforeLaterRequestsAndCorrelatesResponses(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client := codexapp.NewClient(clientSide, nil, nil)
	defer client.Close()

	requests := make(chan codexapp.Request, 2)
	go func() {
		defer close(requests)
		for i := 0; i < 2; i++ {
			req, err := codexapp.ReadRequest(serverSide)
			if err != nil {
				return
			}
			requests <- req
			_ = codexapp.WriteResponse(serverSide, req.ID, map[string]any{"ok": req.Method})
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "test", "version": "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(ctx, "thread/start", map[string]any{"cwd": "/tmp"}); err != nil {
		t.Fatal(err)
	}
	first, second := <-requests, <-requests
	if first.Method != "initialize" || second.Method != "thread/start" {
		t.Fatalf("methods = %q, %q", first.Method, second.Method)
	}
}

func TestClientCorrelatesConcurrentResponsesByRequestID(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client := codexapp.NewClient(clientSide, nil, nil)
	defer client.Close()
	go func() {
		requests := make([]codexapp.Request, 0, 2)
		for len(requests) < 2 {
			request, err := codexapp.ReadRequest(serverSide)
			if err != nil {
				return
			}
			requests = append(requests, request)
		}
		_ = codexapp.WriteResponse(serverSide, requests[1].ID, map[string]string{"method": requests[1].Method})
		_ = codexapp.WriteResponse(serverSide, requests[0].ID, map[string]string{"method": requests[0].Method})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan string, 2)
	var group sync.WaitGroup
	for _, method := range []string{"thread/start", "account/usage/read"} {
		group.Add(1)
		go func(method string) {
			defer group.Done()
			result, err := client.Call(ctx, method, map[string]any{})
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			var decoded struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(result, &decoded)
			results <- decoded.Method
		}(method)
	}
	group.Wait()
	close(results)
	seen := map[string]bool{}
	for result := range results {
		seen[result] = true
	}
	if !seen["thread/start"] || !seen["account/usage/read"] || len(seen) != 2 {
		t.Fatalf("correlated results = %#v", seen)
	}
}

func TestClientRejectsServerRequestWithSameID(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client := codexapp.NewClient(clientSide, nil, nil)
	defer client.Close()
	checked := make(chan struct{})
	go func() {
		_, _ = serverSide.Write([]byte(`{"jsonrpc":"2.0","id":"approval-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1"}}` + "\n"))
		line, err := bufio.NewReader(serverSide).ReadBytes('\n')
		if err != nil {
			return
		}
		var response struct {
			ID    string `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			t.Errorf("decode response: %v", err)
			return
		}
		if response.ID != "approval-1" || response.Error.Code != -32001 {
			t.Errorf("server request response = %#v", response)
		}
		close(checked)
	}()
	deadline := time.After(time.Second)
	select {
	case <-deadline:
		t.Fatal("server request was not rejected")
	case <-checked:
	}
}

func TestClientRoutesServerRequestThroughSerializedResponseWriter(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client := codexapp.NewClient(clientSide, nil, nil, func(event codexapp.Event) (any, error) {
		if event.Method != "item/tool/call" {
			t.Fatalf("method = %q", event.Method)
		}
		return map[string]any{"success": true, "contentItems": []map[string]string{{"type": "inputText", "text": "ok"}}}, nil
	})
	defer client.Close()
	checked := make(chan struct{})
	go func() {
		_, _ = serverSide.Write([]byte(`{"jsonrpc":"2.0","id":9,"method":"item/tool/call","params":{"threadId":"thread-1","turnId":"turn-1"}}` + "\n"))
		line, err := bufio.NewReader(serverSide).ReadBytes('\n')
		if err != nil {
			t.Errorf("read response: %v", err)
			return
		}
		var response struct {
			ID     int `json:"id"`
			Result struct {
				Success bool `json:"success"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &response); err != nil || response.ID != 9 || !response.Result.Success {
			t.Errorf("response=%s err=%v", line, err)
		}
		close(checked)
	}()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("server request was not handled")
	}
}

func TestClientServerRequestHandlerDoesNotBlockResponseCorrelation(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	gate := make(chan struct{})
	client := codexapp.NewClient(clientSide, nil, nil, func(codexapp.Event) (any, error) {
		<-gate
		return map[string]bool{"success": true}, nil
	})
	defer client.Close()
	go func() {
		_, _ = serverSide.Write([]byte(`{"jsonrpc":"2.0","id":"tool-1","method":"item/tool/call","params":{}}` + "\n"))
		request, err := codexapp.ReadRequest(serverSide)
		if err != nil {
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		close(gate)
		_, _ = bufio.NewReader(serverSide).ReadBytes('\n')
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "account/usage/read", map[string]any{}); err != nil {
		t.Fatalf("Call() blocked behind tool request: %v", err)
	}
}
