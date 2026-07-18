package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/router"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

var ErrIgnored = errors.New("ignored feishu event")

const (
	maxCardPayloadBytes          = 28 * 1024
	maxDocumentDescendantBlocks  = 1000
	maxFeishuMessageImageBytes   = 10_000_000
	documentOperationTimeout     = 20 * time.Second
	documentCreateTimeout        = 120 * time.Second
	documentOwnerTransferTimeout = 5 * time.Second
	documentAnnouncementTimeout  = 15 * time.Second
)

type RawEvent struct {
	HeaderAppID  string
	EventID      string
	MessageID    string
	MessageType  string
	ChatType     string
	ChatID       string
	SenderOpenID string
	Content      string
}

func Normalize(expectedAppID string, raw RawEvent) (router.Incoming, error) {
	if raw.HeaderAppID != expectedAppID || raw.EventID == "" || raw.MessageID == "" || raw.ChatType == "topic_group" || raw.ChatType != "p2p" && raw.ChatType != "group" || raw.ChatID == "" {
		return router.Incoming{}, ErrIgnored
	}
	if raw.ChatType == "p2p" && raw.SenderOpenID == "" {
		return router.Incoming{}, ErrIgnored
	}
	incoming := router.Incoming{EventID: raw.EventID, MessageID: raw.MessageID, ChatType: raw.ChatType, ChatID: raw.ChatID, SenderOpenID: raw.SenderOpenID}
	switch raw.MessageType {
	case "text":
		var content struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw.Content), &content); err != nil || content.Text == "" {
			return router.Incoming{}, ErrIgnored
		}
		incoming.Text = content.Text
	case "image":
		var content struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(raw.Content), &content); err != nil || content.ImageKey == "" {
			return router.Incoming{}, ErrIgnored
		}
		incoming.Attachments = []router.AttachmentReference{{Kind: storage.AttachmentImage, ResourceKey: content.ImageKey}}
	case "file":
		var content struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal([]byte(raw.Content), &content); err != nil || content.FileKey == "" {
			return router.Incoming{}, ErrIgnored
		}
		incoming.Attachments = []router.AttachmentReference{{Kind: storage.AttachmentFile, ResourceKey: content.FileKey, OriginalName: content.FileName}}
	default:
		return router.Incoming{}, ErrIgnored
	}
	return incoming, nil
}

func AttachmentResourceType(kind storage.AttachmentKind) (string, bool) {
	switch kind {
	case storage.AttachmentImage:
		return "image", true
	case storage.AttachmentFile:
		return "file", true
	default:
		return "", false
	}
}

type Sender struct {
	appID                 string
	client                *lark.Client
	defaultDocFolderToken string
	mu                    sync.Mutex
	cards                 map[string]*cardSession
}

var feishuSubcodePattern = regexp.MustCompile(`(?:ErrCode|err_code)\s*[:=]\s*(\d+)`)

type feishuAPICallDetail struct {
	Operation  string
	Outcome    string
	Code       int
	Subcode    int
	LogID      string
	DurationMS int64
}

func (d feishuAPICallDetail) String() string {
	return fmt.Sprintf("operation=%s outcome=%s code=%d subcode=%d log_id=%s duration_ms=%d", d.Operation, d.Outcome, d.Code, d.Subcode, d.LogID, d.DurationMS)
}

func feishuAPICallDetails(operation string, response larkcore.CodeError, transportErr error, duration time.Duration) feishuAPICallDetail {
	details := feishuAPICallDetail{Operation: operation, Outcome: "succeeded", Code: response.Code, DurationMS: duration.Milliseconds()}
	if response.Err != nil {
		details.LogID = response.Err.LogID
	}
	if match := feishuSubcodePattern.FindStringSubmatch(response.Msg); len(match) == 2 {
		_, _ = fmt.Sscanf(match[1], "%d", &details.Subcode)
	}
	if transportErr != nil {
		details.Outcome = "transport_failed"
	} else if response.Code != 0 {
		details.Outcome = "rejected"
	}
	return details
}

func (s *Sender) logFeishuAPICall(operation string, startedAt time.Time, response *larkcore.CodeError, transportErr error) {
	codeError := larkcore.CodeError{}
	if response != nil {
		codeError = *response
	}
	details := feishuAPICallDetails(operation, codeError, transportErr, time.Since(startedAt))
	slog.Info("feishu_api_call",
		"event", "feishu_api_call",
		"app_id", s.appID,
		"operation", details.Operation,
		"outcome", details.Outcome,
		"code", details.Code,
		"subcode", details.Subcode,
		"log_id", details.LogID,
		"duration_ms", details.DurationMS,
	)
}

type cardSession struct {
	cardID        string
	sequence      int
	nextOperation time.Time
}

type cardKitRequestError struct {
	operation string
	code      int
}

func (e *cardKitRequestError) Error() string {
	return fmt.Sprintf("cardkit %s code=%d", e.operation, e.code)
}

func NewSender(appID, appSecret string, defaultDocFolderToken ...string) *Sender {
	folder := ""
	if len(defaultDocFolderToken) > 0 {
		folder = defaultDocFolderToken[0]
	}
	return &Sender{appID: appID, client: lark.NewClient(appID, appSecret), defaultDocFolderToken: folder, cards: make(map[string]*cardSession)}
}

// Download retrieves a resource only when invoked by the owning channel
// worker. Receiver callbacks deliberately never call this method.
func (s *Sender) Download(ctx context.Context, messageID, resourceKey string, kind storage.AttachmentKind) (io.ReadCloser, string, error) {
	resourceType, ok := AttachmentResourceType(kind)
	if !ok || messageID == "" || resourceKey == "" {
		return nil, "", fmt.Errorf("attachment resource is invalid")
	}
	startedAt := time.Now()
	resp, err := s.client.Im.V1.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().MessageId(messageID).FileKey(resourceKey).Type(resourceType).Build())
	if resp != nil {
		s.logFeishuAPICall("im.message_resource.get", startedAt, &resp.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message_resource.get", startedAt, nil, err)
	}
	if err != nil {
		return nil, "", fmt.Errorf("download message resource: %w", err)
	}
	if !resp.Success() || resp.File == nil {
		return nil, "", fmt.Errorf("download message resource: code=%d", resp.Code)
	}
	return io.NopCloser(resp.File), resp.FileName, nil
}

func (s *Sender) cardKitAvailable() bool { return true }

func (s *Sender) disableCardKit() {
	slog.Warn("feishu_cardkit_update_fallback", "event", "feishu_cardkit_update_fallback")
}

func (s *Sender) SendText(ctx context.Context, receiveID, receiveType, text string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", fmt.Errorf("marshal text: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(receiveType).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(receiveID).MsgType(larkim.MsgTypeText).Content(string(content)).Build()).Build()
	startedAt := time.Now()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if resp != nil {
		s.logFeishuAPICall("im.message.create.text", startedAt, &resp.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.text", startedAt, nil, err)
	}
	if err != nil {
		return "", fmt.Errorf("create feishu message: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("create feishu message: code=%d", resp.Code)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", errors.New("create feishu message: missing message id")
	}
	return *resp.Data.MessageId, nil
}

// SendStaticCard delivers a bounded, non-streaming command result. The
// caller decides whether an explicit failure may fall back to text; this
// method never retries a possibly visible request.
func (s *Sender) SendStaticCard(ctx context.Context, receiveID, receiveType, text string) (string, error) {
	content, err := json.Marshal(renderBatchCard(text))
	if err != nil {
		return "", fmt.Errorf("marshal static card: %w", err)
	}
	if len(content) > maxCardPayloadBytes {
		return "", fmt.Errorf("static card exceeds %d bytes", maxCardPayloadBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	startedAt := time.Now()
	response, err := s.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(receiveType).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(receiveID).MsgType(larkim.MsgTypeInteractive).Content(string(content)).Build()).Build())
	if response != nil {
		s.logFeishuAPICall("im.message.create.static_card", startedAt, &response.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.static_card", startedAt, nil, err)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", worker.ErrCommandDeliveryUnknown, err)
	}
	if !response.Success() || response.Data == nil || response.Data.MessageId == nil {
		if response == nil {
			return "", fmt.Errorf("%w: empty response", worker.ErrCommandDeliveryUnknown)
		}
		return "", fmt.Errorf("%w: code=%d", worker.ErrCommandDeliveryRejected, response.Code)
	}
	return *response.Data.MessageId, nil
}

func (s *Sender) SendCommandText(ctx context.Context, receiveID, receiveType, text string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", fmt.Errorf("%w: invalid content", worker.ErrCommandDeliveryRejected)
	}
	startedAt := time.Now()
	response, err := s.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(receiveType).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(receiveID).MsgType(larkim.MsgTypeText).Content(string(content)).Build()).Build())
	if response != nil {
		s.logFeishuAPICall("im.message.create.command_text", startedAt, &response.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.command_text", startedAt, nil, err)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", worker.ErrCommandDeliveryUnknown, err)
	}
	if response == nil {
		return "", fmt.Errorf("%w: empty response", worker.ErrCommandDeliveryUnknown)
	}
	if !response.Success() {
		return "", fmt.Errorf("%w: code=%d", worker.ErrCommandDeliveryRejected, response.Code)
	}
	if response.Data == nil || response.Data.MessageId == nil {
		return "", fmt.Errorf("%w: missing message id", worker.ErrCommandDeliveryUnknown)
	}
	return *response.Data.MessageId, nil
}

func (s *Sender) SendCurrentText(ctx context.Context, target worker.ReplyTarget, text string) (string, error) {
	return s.SendText(ctx, target.ID, target.Type, text)
}

// UploadAndSend uses the same opened file descriptor validated by the action
// layer, uploads it once, then sends it only to the already-bound reply target.
func (s *Sender) UploadAndSend(ctx context.Context, target worker.ReplyTarget, file *os.File, fileName string) (string, string, error) {
	if file == nil || fileName == "" || target.ID == "" || target.Type == "" {
		return "", "", fmt.Errorf("file send parameters are invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, documentOperationTimeout)
	defer cancel()
	isImage, err := isFeishuMessageImage(file)
	if err != nil {
		return "", "", fmt.Errorf("inspect upload payload: %w", err)
	}
	if isImage {
		return s.uploadImageAndSend(ctx, target, file)
	}
	fileType := "stream"
	startedAt := time.Now()
	upload, err := s.client.Im.V1.File.Create(ctx, larkim.NewCreateFileReqBuilder().Body(&larkim.CreateFileReqBody{FileType: &fileType, FileName: &fileName, File: file}).Build())
	if upload != nil {
		s.logFeishuAPICall("im.file.create", startedAt, &upload.CodeError, err)
	} else {
		s.logFeishuAPICall("im.file.create", startedAt, nil, err)
	}
	if err != nil {
		return "", "", fmt.Errorf("upload feishu file: %w", err)
	}
	if !upload.Success() || upload.Data == nil || upload.Data.FileKey == nil {
		return "", "", fmt.Errorf("upload feishu file: code=%d", upload.Code)
	}
	content, err := json.Marshal(map[string]string{"file_key": *upload.Data.FileKey})
	if err != nil {
		return "", "", fmt.Errorf("marshal file message: %w", err)
	}
	startedAt = time.Now()
	message, err := s.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(target.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(target.ID).MsgType(larkim.MsgTypeFile).Content(string(content)).Build()).Build())
	if message != nil {
		s.logFeishuAPICall("im.message.create.file", startedAt, &message.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.file", startedAt, nil, err)
	}
	if err != nil {
		return "", "", fmt.Errorf("send feishu file: %w", err)
	}
	if !message.Success() || message.Data == nil || message.Data.MessageId == nil {
		return "", "", fmt.Errorf("send feishu file: code=%d", message.Code)
	}
	return *upload.Data.FileKey, *message.Data.MessageId, nil
}

func (s *Sender) uploadImageAndSend(ctx context.Context, target worker.ReplyTarget, file *os.File) (string, string, error) {
	imageType := larkim.CreateImageImageTypeMessage
	startedAt := time.Now()
	upload, err := s.client.Im.V1.Image.Create(ctx, larkim.NewCreateImageReqBuilder().Body(larkim.NewCreateImageReqBodyBuilder().ImageType(imageType).Image(file).Build()).Build())
	if upload != nil {
		s.logFeishuAPICall("im.image.create", startedAt, &upload.CodeError, err)
	} else {
		s.logFeishuAPICall("im.image.create", startedAt, nil, err)
	}
	if err != nil {
		return "", "", fmt.Errorf("upload feishu image: %w", err)
	}
	if !upload.Success() || upload.Data == nil || upload.Data.ImageKey == nil {
		return "", "", fmt.Errorf("upload feishu image: code=%d", upload.Code)
	}
	content, err := json.Marshal(map[string]string{"image_key": *upload.Data.ImageKey})
	if err != nil {
		return "", "", fmt.Errorf("marshal image message: %w", err)
	}
	startedAt = time.Now()
	message, err := s.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(target.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(target.ID).MsgType(larkim.MsgTypeImage).Content(string(content)).Build()).Build())
	if message != nil {
		s.logFeishuAPICall("im.message.create.image", startedAt, &message.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.image", startedAt, nil, err)
	}
	if err != nil {
		return "", "", fmt.Errorf("send feishu image: %w", err)
	}
	if !message.Success() || message.Data == nil || message.Data.MessageId == nil {
		return "", "", fmt.Errorf("send feishu image: code=%d", message.Code)
	}
	return *upload.Data.ImageKey, *message.Data.MessageId, nil
}

// isFeishuMessageImage detects the image formats supported by Feishu's image
// upload API. ReadAt deliberately leaves the already-validated descriptor at
// offset zero for whichever upload path follows.
func isFeishuMessageImage(file *os.File) (bool, error) {
	if file == nil {
		return false, errors.New("file is nil")
	}
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() > maxFeishuMessageImageBytes {
		return false, nil
	}
	var header [12]byte
	n, readErr := file.ReadAt(header[:], 0)
	if readErr != nil && readErr != io.EOF {
		return false, readErr
	}
	return hasFeishuImageSignature(header[:n]), nil
}

func hasFeishuImageSignature(header []byte) bool {
	if len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff { // JPEG
		return true
	}
	if len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n" { // PNG
		return true
	}
	if len(header) >= 6 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a") { // GIF
		return true
	}
	if len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" { // WEBP
		return true
	}
	if len(header) >= 4 && (string(header[:4]) == "II*\x00" || string(header[:4]) == "MM\x00*") { // TIFF
		return true
	}
	if len(header) >= 2 && string(header[:2]) == "BM" { // BMP
		return true
	}
	return len(header) >= 4 && header[0] == 0 && header[1] == 0 && header[2] == 1 && header[3] == 0 // ICO
}

// CreateDocumentAndAnnounce creates the current App's document and sends its
// URL to the same trusted reply target. Content conversion is deliberately
// kept in the action boundary; the URL is announced even if later content
// writing reports a partial outcome.
func (s *Sender) CreateDocumentAndAnnounce(ctx context.Context, target worker.ReplyTarget, ownerOpenID, title string, markdown []byte) (worker.DocumentOutcome, error) {
	if target.ID == "" || target.Type == "" || ownerOpenID == "" || len(markdown) == 0 {
		return worker.DocumentOutcome{}, fmt.Errorf("document parameters are invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, documentCreateTimeout)
	defer cancel()
	body := larkdocx.NewCreateDocumentReqBodyBuilder().Title(title)
	if s.defaultDocFolderToken != "" {
		body.FolderToken(s.defaultDocFolderToken)
	}
	request := larkdocx.NewCreateDocumentReqBuilder().Body(body.Build()).Build()
	startedAt := time.Now()
	response, err := s.client.Docx.V1.Document.Create(ctx, request)
	if response != nil {
		s.logFeishuAPICall("docx.document.create", startedAt, &response.CodeError, err)
	} else {
		s.logFeishuAPICall("docx.document.create", startedAt, nil, err)
	}
	if err != nil {
		return worker.DocumentOutcome{}, fmt.Errorf("create document: %w", err)
	}
	if !response.Success() || response.Data == nil || response.Data.Document == nil || response.Data.Document.DocumentId == nil {
		return worker.DocumentOutcome{}, fmt.Errorf("create document: code=%d", response.Code)
	}
	documentID := *response.Data.Document.DocumentId
	url := "https://feishu.cn/docx/" + documentID
	transferOwner := func() (bool, string) {
		// Removing the App's old-owner permission can revoke its ability to
		// finish Docx writes. Transfer only after the content attempt, but
		// before the visible URL announcement, so every created document still
		// gets exactly one immediate best-effort owner transfer.
		transferCtx, cancelTransfer := documentPostCreateContext(ctx, documentOwnerTransferTimeout)
		defer cancelTransfer()
		return transferDocumentOwner(transferCtx, larkOwnerTransferer{client: s.client.Drive.V1.PermissionMember, logAPICall: s.logFeishuAPICall}, documentID, ownerOpenID)
	}
	startedAt = time.Now()
	converted, convertErr := s.client.Docx.V1.Document.Convert(ctx, larkdocx.NewConvertDocumentReqBuilder().Body(larkdocx.NewConvertDocumentReqBodyBuilder().ContentType(larkdocx.ContentTypeMarkdown).Content(string(markdown)).Build()).Build())
	if converted != nil {
		s.logFeishuAPICall("docx.document.convert", startedAt, &converted.CodeError, convertErr)
	} else {
		s.logFeishuAPICall("docx.document.convert", startedAt, nil, convertErr)
	}
	if convertErr != nil || converted == nil || !converted.Success() || converted.Data == nil || len(converted.Data.FirstLevelBlockIds) == 0 || len(converted.Data.Blocks) == 0 {
		ownerTransferred, ownerTransferOutcome := transferOwner()
		if announceErr := s.announceDocumentURL(ctx, target, url); announceErr != nil {
			return worker.DocumentOutcome{}, fmt.Errorf("announce document after markdown conversion failure: %w", announceErr)
		}
		return worker.DocumentOutcome{URL: url, ContentWritten: false, AnnouncementOutcome: "sent", OwnerTransferred: ownerTransferred, OwnerTransferOutcome: ownerTransferOutcome}, nil
	}
	sanitizeConvertedBlocks(converted.Data.Blocks)
	batches, batchErr := planDocumentBlockDescendantBatches(converted.Data.FirstLevelBlockIds, converted.Data.Blocks, maxDocumentDescendantBlocks)
	if batchErr != nil {
		ownerTransferred, ownerTransferOutcome := transferOwner()
		if announceErr := s.announceDocumentURL(ctx, target, url); announceErr != nil {
			return worker.DocumentOutcome{}, fmt.Errorf("announce partially written document: %w", announceErr)
		}
		return worker.DocumentOutcome{URL: url, ContentWritten: false, AnnouncementOutcome: "sent", OwnerTransferred: ownerTransferred, OwnerTransferOutcome: ownerTransferOutcome}, nil
	}
	for _, batch := range batches {
		write := larkdocx.NewCreateDocumentBlockDescendantReqBuilder().DocumentId(*response.Data.Document.DocumentId).BlockId(*response.Data.Document.DocumentId).DocumentRevisionId(-1).Body(larkdocx.NewCreateDocumentBlockDescendantReqBodyBuilder().ChildrenId(batch.ChildrenID).Descendants(batch.Descendants).Index(-1).Build()).Build()
		startedAt = time.Now()
		writeResponse, writeErr := s.client.Docx.V1.DocumentBlockDescendant.Create(ctx, write)
		if writeResponse != nil {
			s.logFeishuAPICall("docx.document_block_descendant.create", startedAt, &writeResponse.CodeError, writeErr)
		} else {
			s.logFeishuAPICall("docx.document_block_descendant.create", startedAt, nil, writeErr)
		}
		if writeErr != nil || writeResponse == nil || !writeResponse.Success() {
			ownerTransferred, ownerTransferOutcome := transferOwner()
			if announceErr := s.announceDocumentURL(ctx, target, url); announceErr != nil {
				return worker.DocumentOutcome{}, fmt.Errorf("announce partially written document: %w", announceErr)
			}
			return worker.DocumentOutcome{URL: url, ContentWritten: false, AnnouncementOutcome: "sent", OwnerTransferred: ownerTransferred, OwnerTransferOutcome: ownerTransferOutcome}, nil
		}
	}
	ownerTransferred, ownerTransferOutcome := transferOwner()
	if err := s.announceDocumentURL(ctx, target, url); err != nil {
		return worker.DocumentOutcome{URL: url, ContentWritten: true, AnnouncementOutcome: "unknown", OwnerTransferred: ownerTransferred, OwnerTransferOutcome: ownerTransferOutcome}, nil
	}
	return worker.DocumentOutcome{URL: url, ContentWritten: true, AnnouncementOutcome: "sent", OwnerTransferred: ownerTransferred, OwnerTransferOutcome: ownerTransferOutcome}, nil
}

func documentPostCreateContext(operationCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	// A best-effort transfer must never consume the announcement's entire
	// budget. WithoutCancel retains trace values while isolating this
	// post-create delivery from an exhausted write operation deadline.
	return context.WithTimeout(context.WithoutCancel(operationCtx), timeout)
}

func (s *Sender) announceDocumentURL(operationCtx context.Context, target worker.ReplyTarget, url string) error {
	announcementCtx, cancelAnnouncement := documentPostCreateContext(operationCtx, documentAnnouncementTimeout)
	defer cancelAnnouncement()
	_, err := s.SendText(announcementCtx, target.ID, target.Type, url)
	return err
}

type permissionOwnerTransferClient interface {
	TransferOwner(context.Context, *larkdrive.TransferOwnerPermissionMemberReq, ...larkcore.RequestOptionFunc) (*larkdrive.TransferOwnerPermissionMemberResp, error)
}

type ownerTransferer interface {
	TransferOwner(context.Context, string, string) (bool, string)
}

type larkOwnerTransferer struct {
	client     permissionOwnerTransferClient
	logAPICall func(string, time.Time, *larkcore.CodeError, error)
}

func (t larkOwnerTransferer) TransferOwner(ctx context.Context, documentID, ownerOpenID string) (bool, string) {
	if t.client == nil || documentID == "" || ownerOpenID == "" {
		return false, "not_attempted"
	}
	request := larkdrive.NewTransferOwnerPermissionMemberReqBuilder().
		Token(documentID).
		Type(larkdrive.TokenTypeTransferOwnerPermissionMemberDocx).
		NeedNotification(false).
		RemoveOldOwner(true).
		Owner(larkdrive.NewOwnerBuilder().
			MemberType(larkdrive.MemberTypeTransferOwnerPermissionMemberOpenId).
			MemberId(ownerOpenID).
			Build()).
		Build()
	startedAt := time.Now()
	response, err := t.client.TransferOwner(ctx, request)
	if t.logAPICall != nil {
		if response != nil {
			t.logAPICall("drive.permission_member.transfer_owner", startedAt, &response.CodeError, err)
		} else {
			t.logAPICall("drive.permission_member.transfer_owner", startedAt, nil, err)
		}
	}
	if err != nil || response == nil {
		return false, "unknown"
	}
	if !response.Success() {
		return false, "rejected"
	}
	return true, "transferred"
}

func transferDocumentOwner(ctx context.Context, client ownerTransferer, documentID, ownerOpenID string) (bool, string) {
	if client == nil || documentID == "" || ownerOpenID == "" {
		return false, "not_attempted"
	}
	return client.TransferOwner(ctx, documentID, ownerOpenID)
}

// ReadDocument retrieves only the plain text for a document the current App
// can already access. Its caller owns delivery to the bound Codex Turn.
func (s *Sender) ReadDocument(ctx context.Context, documentID string) (string, error) {
	if documentID == "" {
		return "", fmt.Errorf("document id is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	startedAt := time.Now()
	response, err := s.client.Docx.V1.Document.RawContent(ctx, larkdocx.NewRawContentDocumentReqBuilder().DocumentId(documentID).Build())
	if response != nil {
		s.logFeishuAPICall("docx.document.raw_content", startedAt, &response.CodeError, err)
	} else {
		s.logFeishuAPICall("docx.document.raw_content", startedAt, nil, err)
	}
	if err != nil {
		return "", fmt.Errorf("read document: %w", err)
	}
	if response == nil || !response.Success() || response.Data == nil || response.Data.Content == nil {
		if response == nil {
			return "", errors.New("read document: empty response")
		}
		return "", fmt.Errorf("read document: code=%d", response.Code)
	}
	return *response.Data.Content, nil
}

func sanitizeConvertedBlocks(blocks []*larkdocx.Block) {
	for _, block := range blocks {
		if block != nil && block.Table != nil && block.Table.Property != nil {
			block.Table.Property.MergeInfo = nil
		}
	}
}

type documentBlockDescendantBatch struct {
	ChildrenID  []string
	Descendants []*larkdocx.Block
}

func planDocumentBlockDescendantBatches(rootIDs []string, blocks []*larkdocx.Block, limit int) ([]documentBlockDescendantBatch, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("document descendant batch limit is invalid")
	}
	byID := make(map[string]*larkdocx.Block, len(blocks))
	for _, block := range blocks {
		if block == nil || block.BlockId == nil || *block.BlockId == "" {
			return nil, fmt.Errorf("converted document contains block without id")
		}
		if _, exists := byID[*block.BlockId]; exists {
			return nil, fmt.Errorf("converted document contains duplicate block id")
		}
		byID[*block.BlockId] = block
	}
	assigned := make(map[string]bool, len(byID))
	batch := documentBlockDescendantBatch{}
	var batches []documentBlockDescendantBatch
	flush := func() {
		if len(batch.ChildrenID) == 0 {
			return
		}
		batches = append(batches, batch)
		batch = documentBlockDescendantBatch{}
	}
	for _, rootID := range rootIDs {
		if rootID == "" {
			return nil, fmt.Errorf("converted document contains empty root block id")
		}
		subtreeIDs, err := convertedDocumentBlockSubtree(rootID, byID)
		if err != nil {
			return nil, err
		}
		if len(subtreeIDs) > limit {
			return nil, fmt.Errorf("converted document root block exceeds descendant batch limit: block_count=%d limit=%d", len(subtreeIDs), limit)
		}
		if len(batch.Descendants) > 0 && len(batch.Descendants)+len(subtreeIDs) > limit {
			flush()
		}
		batch.ChildrenID = append(batch.ChildrenID, rootID)
		for _, id := range subtreeIDs {
			if assigned[id] {
				return nil, fmt.Errorf("converted document block belongs to multiple roots")
			}
			assigned[id] = true
			batch.Descendants = append(batch.Descendants, byID[id])
		}
	}
	flush()
	if len(assigned) != len(byID) {
		return nil, fmt.Errorf("converted document contains descendants outside its root blocks")
	}
	return batches, nil
}

func convertedDocumentBlockSubtree(rootID string, byID map[string]*larkdocx.Block) ([]string, error) {
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var walk func(string) ([]string, error)
	walk = func(id string) ([]string, error) {
		if visiting[id] {
			return nil, fmt.Errorf("converted document contains cyclic block hierarchy")
		}
		if visited[id] {
			return nil, fmt.Errorf("converted document block has multiple parents")
		}
		block, exists := byID[id]
		if !exists {
			return nil, fmt.Errorf("converted document references missing child block")
		}
		visiting[id] = true
		result := []string{id}
		for _, childID := range block.Children {
			childIDs, err := walk(childID)
			if err != nil {
				return nil, err
			}
			result = append(result, childIDs...)
		}
		delete(visiting, id)
		visited[id] = true
		return result, nil
	}
	return walk(rootID)
}

func (s *Sender) CreateBatchCard(ctx context.Context, batch worker.Batch) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	progress := initialWorkCardProgress(batch.Goal)
	content, err := RenderWorkCardJSON("*思考中…*", progress, "生成中…", false)
	if err != nil {
		return "", fmt.Errorf("marshal batch card: %w", err)
	}
	if len(content) > maxCardPayloadBytes {
		return "", fmt.Errorf("batch card payload exceeds %d bytes", maxCardPayloadBytes)
	}
	var cardResp *larkcardkit.CreateCardResp
	if s.cardKitAvailable() {
		startedAt := time.Now()
		cardResp, err = s.client.Cardkit.V1.Card.Create(ctx, larkcardkit.NewCreateCardReqBuilder().Body(larkcardkit.NewCreateCardReqBodyBuilder().Type("card_json").Data(string(content)).Build()).Build())
		if cardResp != nil {
			s.logFeishuAPICall("cardkit.card.create", startedAt, &cardResp.CodeError, err)
		} else {
			s.logFeishuAPICall("cardkit.card.create", startedAt, nil, err)
		}
	}
	if err == nil && cardResp != nil && cardResp.Success() && cardResp.Data != nil && cardResp.Data.CardId != nil {
		content, err = json.Marshal(map[string]any{"type": "card", "data": map[string]string{"card_id": *cardResp.Data.CardId}})
		if err != nil {
			return "", fmt.Errorf("marshal cardkit message: %w", err)
		}
	} else {
		content, err = RenderWorkCardJSON("*思考中…*", progress, "生成中…", false)
		if err != nil {
			return "", fmt.Errorf("marshal patch fallback card: %w", err)
		}
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(batch.Messages[0].Reply.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(batch.Messages[0].Reply.ID).MsgType(larkim.MsgTypeInteractive).Content(string(content)).Build()).Build()
	startedAt := time.Now()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if resp != nil {
		s.logFeishuAPICall("im.message.create.work_card", startedAt, &resp.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.work_card", startedAt, nil, err)
	}
	if err != nil {
		return "", fmt.Errorf("create batch card: %w", err)
	}
	if !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("create batch card: code=%d", resp.Code)
	}
	messageID := *resp.Data.MessageId
	if cardResp != nil && cardResp.Success() && cardResp.Data != nil && cardResp.Data.CardId != nil {
		s.mu.Lock()
		s.cards[messageID] = &cardSession{cardID: *cardResp.Data.CardId}
		s.mu.Unlock()
	}
	slog.Info("feishu_card_created", "event", "feishu_card_created", "batch_id", batch.ID, "mode", map[bool]string{true: "cardkit", false: "patch"}[cardResp != nil && cardResp.Success() && cardResp.Data != nil && cardResp.Data.CardId != nil])
	return messageID, nil
}

func initialWorkCardProgress(goal bool) string {
	if goal {
		return "目标已受理，正在启动 Codex…"
	}
	return "等待 Codex 进展…"
}

func (s *Sender) UpdateBatchCard(ctx context.Context, messageID, content string) error {
	card, err := RenderWorkCardJSON(content, "", "已完成", true)
	if err != nil {
		return fmt.Errorf("marshal batch update: %w", err)
	}
	return s.patchCard(ctx, messageID, card)
}

func (s *Sender) UpdateBatchCardZones(ctx context.Context, messageID, final, progress, summary string, closed bool) error {
	if s.cardKitAvailable() {
		if err := s.updateCardKitEntity(ctx, messageID, final, progress, summary, closed); err == nil {
			return nil
		} else {
			fields := cardKitFailureFields(err)
			slog.Warn("feishu_cardkit_fallback", "event", "feishu_cardkit_fallback", "reason", stableCardKitReason(err), "operation", fields.operation, "code", fields.code)
			s.disableCardKit()
		}
	}
	card, err := RenderWorkCardJSON(final, progress, summary, closed)
	if err != nil {
		return fmt.Errorf("marshal batch zones update: %w", err)
	}
	return s.patchCard(ctx, messageID, card)
}

func (s *Sender) updateCardKitEntity(ctx context.Context, messageID, final, progress, summary string, closed bool) error {
	card, err := cardKitUpdateCard(final, progress, closed)
	if err != nil {
		return err
	}
	if card.Data == nil || len(*card.Data) > maxCardPayloadBytes {
		return fmt.Errorf("cardkit payload exceeds %d bytes", maxCardPayloadBytes)
	}
	s.mu.Lock()
	session := s.cards[messageID]
	if session == nil {
		s.mu.Unlock()
		return errors.New("cardkit session unavailable")
	}
	cardID := session.cardID
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sequence, wait := s.nextCardKitOperation(messageID)
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	startedAt := time.Now()
	resp, err := s.client.Cardkit.V1.Card.Update(ctx, larkcardkit.NewUpdateCardReqBuilder().CardId(cardID).Body(larkcardkit.NewUpdateCardReqBodyBuilder().Card(card).Uuid(fmt.Sprintf("%s-%d", messageID, sequence)).Sequence(sequence).Build()).Build())
	if resp != nil {
		s.logFeishuAPICall("cardkit.card.update", startedAt, &resp.CodeError, err)
	} else {
		s.logFeishuAPICall("cardkit.card.update", startedAt, nil, err)
	}
	if err != nil || resp == nil || !resp.Success() {
		s.dropCardSession(messageID)
		if err != nil {
			return err
		}
		if resp == nil {
			return errors.New("cardkit full_update returned an empty response")
		}
		return &cardKitRequestError{operation: "full_update", code: resp.Code}
	}
	slog.Info("feishu_card_updated", "event", "feishu_card_updated", "mode", "cardkit_full", "closed", closed)
	return nil
}

func cardKitUpdateCard(final, progress string, closed bool) (*larkcardkit.Card, error) {
	data, err := RenderWorkCardJSON(final, progress, "", closed)
	if err != nil {
		return nil, err
	}
	return larkcardkit.NewCardBuilder().Type("card_json").Data(string(data)).Build(), nil
}

func stableCardKitReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "request_failed"
}

func cardKitFailureFields(err error) cardKitRequestError {
	var requestError *cardKitRequestError
	if errors.As(err, &requestError) {
		return *requestError
	}
	return cardKitRequestError{operation: "transport", code: 0}
}

func (s *Sender) nextCardKitOperation(messageID string) (int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.cards[messageID]
	if session == nil {
		return 0, 0
	}
	now := time.Now()
	if session.nextOperation.IsZero() || !now.Before(session.nextOperation) {
		session.nextOperation = now.Add(100 * time.Millisecond)
	} else {
		wait := time.Until(session.nextOperation)
		session.nextOperation = session.nextOperation.Add(100 * time.Millisecond)
		session.sequence++
		return session.sequence, wait
	}
	session.sequence++
	return session.sequence, 0
}

func (s *Sender) dropCardSession(messageID string) {
	// A failed entity update does not prove that the entity vanished. Keep its
	// card ID so the next projection flush can retry CardKit instead of forcing
	// the whole message into PATCH mode.
	slog.Debug("feishu_cardkit_session_retained", "event", "feishu_cardkit_session_retained")
}

func (s *Sender) patchCard(ctx context.Context, messageID string, card []byte) error {
	if len(card) > maxCardPayloadBytes {
		return fmt.Errorf("patch payload exceeds %d bytes", maxCardPayloadBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req := larkim.NewPatchMessageReqBuilder().MessageId(messageID).Body(larkim.NewPatchMessageReqBodyBuilder().Content(string(card)).Build()).Build()
	startedAt := time.Now()
	resp, err := s.client.Im.Message.Patch(ctx, req)
	if resp != nil {
		s.logFeishuAPICall("im.message.patch", startedAt, &resp.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.patch", startedAt, nil, err)
	}
	if err != nil {
		return fmt.Errorf("update batch card: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("update batch card: code=%d", resp.Code)
	}
	slog.Info("feishu_card_updated", "event", "feishu_card_updated", "mode", "patch")
	return nil
}

func (s *Sender) SendBatchText(ctx context.Context, target worker.ReplyTarget, text string) (string, error) {
	return s.SendText(ctx, target.ID, target.Type, text)
}

func (s *Sender) SendCompanionSegment(ctx context.Context, target worker.ReplyTarget, text string) worker.CompanionSendResult {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return worker.CompanionSendResult{Outcome: worker.CompanionRejected, Reason: "invalid_content"}
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(target.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(target.ID).MsgType(larkim.MsgTypeText).Content(string(content)).Build()).Build()
	startedAt := time.Now()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if resp != nil {
		s.logFeishuAPICall("im.message.create.companion_segment", startedAt, &resp.CodeError, err)
	} else {
		s.logFeishuAPICall("im.message.create.companion_segment", startedAt, nil, err)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return worker.CompanionSendResult{Outcome: worker.CompanionCancelled, Reason: "cancelled"}
		}
		return worker.CompanionSendResult{Outcome: worker.CompanionUnknown, Reason: "request_unknown"}
	}
	if !resp.Success() {
		if resp.Code == 429 || resp.Code == 99991663 {
			return worker.CompanionSendResult{Outcome: worker.CompanionRejected, Reason: "rate_limited"}
		}
		return worker.CompanionSendResult{Outcome: worker.CompanionRejected, Reason: "rejected"}
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return worker.CompanionSendResult{Outcome: worker.CompanionUnknown, Reason: "missing_message_id"}
	}
	return worker.CompanionSendResult{MessageID: *resp.Data.MessageId, Outcome: worker.CompanionSent}
}

func renderBatchCard(markdown string) map[string]any {
	return map[string]any{"config": map[string]any{"wide_screen_mode": true}, "elements": []map[string]any{{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": markdown}}}}
}

func RenderWorkCardJSON(final, progress, summary string, closed bool) ([]byte, error) {
	_ = summary // UI intentionally uses no status/title; typography separates the two zones.
	if final == "" {
		final = "*思考中…*"
	}
	if progress == "" {
		progress = "等待 Codex 进展…"
	}
	progress = progressMarkdown(progress)
	return json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": !closed,
			"update_multi":   true,
		},
		"body": map[string]any{"elements": []map[string]any{
			{"tag": "markdown", "element_id": "progress_text", "content": progress, "font_color": "grey", "text_size": "notation"},
			{"tag": "hr"},
			{"tag": "markdown", "element_id": "final_text", "content": final},
		}},
	})
}

func progressMarkdown(progress string) string {
	return "*" + progress + "*"
}

type Receiver struct {
	app     storage.App
	handler *router.Handler
	client  *larkws.Client
	status  func(string)
}

func NewReceiver(app storage.App, handler *router.Handler) *Receiver {
	return &Receiver{app: app, handler: handler}
}
func NewReceiverWithStatus(app storage.App, handler *router.Handler, status func(string)) *Receiver {
	return &Receiver{app: app, handler: handler, status: status}
}

func (r *Receiver) Start(ctx context.Context) error {
	events := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(r.handleMessage)
	r.client = larkws.NewClient(r.app.FeishuAppID, r.app.FeishuAppSecret, larkws.WithEventHandler(events), larkws.WithLogLevel(larkcore.LogLevelError), larkws.WithOnReady(func() {
		if r.status != nil {
			r.status("connected")
		}
		slog.Info("feishu_connected", "app_id", r.app.ID, "event", "feishu_connected")
	}))
	go func() { <-ctx.Done(); r.client.Close() }()
	return r.client.Start(ctx)
}

func (r *Receiver) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.EventV2Base == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil || event.Event.Sender.SenderId == nil || event.EventV2Base.Header == nil {
		return nil
	}
	msg := event.Event.Message
	incoming, err := Normalize(r.app.FeishuAppID, RawEvent{HeaderAppID: event.EventV2Base.Header.AppID, EventID: event.EventV2Base.Header.EventID, MessageID: stringValue(msg.MessageId), MessageType: stringValue(msg.MessageType), ChatType: stringValue(msg.ChatType), ChatID: stringValue(msg.ChatId), SenderOpenID: stringValue(event.Event.Sender.SenderId.OpenId), Content: stringValue(msg.Content)})
	if err != nil {
		slog.Info("feishu_event_ignored", "app_id", r.app.ID, "event", "feishu_event_ignored")
		return nil
	}
	incoming.App = router.App{ID: r.app.ID, Name: r.app.Name}
	if err := r.handler.Handle(ctx, incoming); err != nil && !errors.Is(err, router.ErrDuplicate) {
		return err
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
