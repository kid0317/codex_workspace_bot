package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
)

func TestFeishuAPICallDetailsRedactsMessageAndPreservesDiagnostics(t *testing.T) {
	var response larkcore.CodeError
	if err := json.Unmarshal([]byte(`{"code":230099,"msg":"Failed to create card content, ext=ErrCode: 11310; ErrMsg: element exceeds the limit","error":{"log_id":"platform-log-1"}}`), &response); err != nil {
		t.Fatal(err)
	}
	details := feishuAPICallDetails("im.message.create", response, nil, 12*time.Millisecond)
	if details.Operation != "im.message.create" || details.Outcome != "rejected" || details.Code != 230099 || details.Subcode != 11310 || details.LogID != "platform-log-1" || details.DurationMS != 12 {
		t.Fatalf("details=%#v", details)
	}
	if rendered := details.String(); strings.Contains(rendered, "element exceeds") || strings.Contains(rendered, "ErrMsg") {
		t.Fatalf("unsafe API message leaked into details: %s", rendered)
	}
}

type fakePermissionOwnerTransferer struct {
	documentID  string
	ownerOpenID string
	transferred bool
	outcome     string
}

func (f *fakePermissionOwnerTransferer) TransferOwner(_ context.Context, documentID, ownerOpenID string) (bool, string) {
	f.documentID, f.ownerOpenID = documentID, ownerOpenID
	return f.transferred, f.outcome
}

type fakePermissionOwnerTransferClient struct {
	response *larkdrive.TransferOwnerPermissionMemberResp
	err      error
}

func (f fakePermissionOwnerTransferClient) TransferOwner(_ context.Context, _ *larkdrive.TransferOwnerPermissionMemberReq, _ ...larkcore.RequestOptionFunc) (*larkdrive.TransferOwnerPermissionMemberResp, error) {
	return f.response, f.err
}

func TestInitialWorkCardProgressDistinguishesGoalStartup(t *testing.T) {
	if got := initialWorkCardProgress(true); got != "目标已受理，正在启动 Codex…" {
		t.Fatalf("goal startup progress = %q", got)
	}
	if got := initialWorkCardProgress(false); got != "等待 Codex 进展…" {
		t.Fatalf("normal startup progress = %q", got)
	}
}

func TestTransferDocumentOwnerUsesDocxAndTrustedOpenID(t *testing.T) {
	client := &fakePermissionOwnerTransferer{transferred: true, outcome: "transferred"}
	transferred, outcome := transferDocumentOwner(context.Background(), client, "docx-token", "ou-sender")
	if !transferred || outcome != "transferred" {
		t.Fatalf("transferred=%t outcome=%q", transferred, outcome)
	}
	if client.documentID != "docx-token" || client.ownerOpenID != "ou-sender" {
		t.Fatalf("transfer input document=%q owner=%q", client.documentID, client.ownerOpenID)
	}
}

func TestTransferDocumentOwnerClassifiesRejectedAndUnknown(t *testing.T) {
	tests := []struct {
		name        string
		client      *fakePermissionOwnerTransferer
		transferred bool
		outcome     string
	}{
		{name: "rejected", client: &fakePermissionOwnerTransferer{outcome: "rejected"}, outcome: "rejected"},
		{name: "transport failure", client: &fakePermissionOwnerTransferer{outcome: "unknown"}, outcome: "unknown"},
		{name: "empty response", client: &fakePermissionOwnerTransferer{outcome: "unknown"}, outcome: "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transferred, outcome := transferDocumentOwner(context.Background(), tc.client, "docx-token", "ou-sender")
			if transferred != tc.transferred || outcome != tc.outcome {
				t.Fatalf("transferred=%t outcome=%q", transferred, outcome)
			}
		})
	}
	if transferred, outcome := transferDocumentOwner(context.Background(), nil, "", ""); transferred || outcome != "not_attempted" {
		t.Fatalf("missing transferer result = %t %q", transferred, outcome)
	}
}

func TestLarkOwnerTransfererClassifiesAPISuccessAndFailure(t *testing.T) {
	tests := []struct {
		name        string
		client      fakePermissionOwnerTransferClient
		transferred bool
		outcome     string
	}{
		{name: "success", client: fakePermissionOwnerTransferClient{response: &larkdrive.TransferOwnerPermissionMemberResp{CodeError: larkcore.CodeError{Code: 0}}}, transferred: true, outcome: "transferred"},
		{name: "rejected", client: fakePermissionOwnerTransferClient{response: &larkdrive.TransferOwnerPermissionMemberResp{CodeError: larkcore.CodeError{Code: 999}}}, outcome: "rejected"},
		{name: "network", client: fakePermissionOwnerTransferClient{err: errors.New("timeout")}, outcome: "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transferred, outcome := larkOwnerTransferer{client: tc.client}.TransferOwner(context.Background(), "docx-token", "ou-sender")
			if transferred != tc.transferred || outcome != tc.outcome {
				t.Fatalf("transferred=%t outcome=%q", transferred, outcome)
			}
		})
	}
}

func TestDocumentPostCreateContextSurvivesOperationCancellation(t *testing.T) {
	operationCtx, cancelOperation := context.WithCancel(context.Background())
	cancelOperation()
	postCreateCtx, cancelPostCreate := documentPostCreateContext(operationCtx, time.Second)
	defer cancelPostCreate()
	if err := postCreateCtx.Err(); err != nil {
		t.Fatalf("post-create context inherited operation cancellation: %v", err)
	}
	select {
	case <-postCreateCtx.Done():
		t.Fatalf("post-create context ended too early: %v", postCreateCtx.Err())
	default:
	}
}
