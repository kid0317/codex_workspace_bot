package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

var ErrIgnored = errors.New("ignored feishu event")

const (
	maxCardPayloadBytes         = 28 * 1024
	maxDocumentDescendantBlocks = 1000
	maxFeishuMessageImageBytes  = 10_000_000
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
	client                *lark.Client
	defaultDocFolderToken string
	mu                    sync.Mutex
	cards                 map[string]*cardSession
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
	return &Sender{client: lark.NewClient(appID, appSecret), defaultDocFolderToken: folder, cards: make(map[string]*cardSession)}
}

// Download retrieves a resource only when invoked by the owning channel
// worker. Receiver callbacks deliberately never call this method.
func (s *Sender) Download(ctx context.Context, messageID, resourceKey string, kind storage.AttachmentKind) (io.ReadCloser, string, error) {
	resourceType, ok := AttachmentResourceType(kind)
	if !ok || messageID == "" || resourceKey == "" {
		return nil, "", fmt.Errorf("attachment resource is invalid")
	}
	resp, err := s.client.Im.V1.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().MessageId(messageID).FileKey(resourceKey).Type(resourceType).Build())
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
	resp, err := s.client.Im.Message.Create(ctx, req)
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

func (s *Sender) SendCurrentText(ctx context.Context, target worker.ReplyTarget, text string) (string, error) {
	return s.SendText(ctx, target.ID, target.Type, text)
}

// UploadAndSend uses the same opened file descriptor validated by the action
// layer, uploads it once, then sends it only to the already-bound reply target.
func (s *Sender) UploadAndSend(ctx context.Context, target worker.ReplyTarget, file *os.File, fileName string) (string, string, error) {
	if file == nil || fileName == "" || target.ID == "" || target.Type == "" {
		return "", "", fmt.Errorf("file send parameters are invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	isImage, err := isFeishuMessageImage(file)
	if err != nil {
		return "", "", fmt.Errorf("inspect upload payload: %w", err)
	}
	if isImage {
		return s.uploadImageAndSend(ctx, target, file)
	}
	fileType := "stream"
	upload, err := s.client.Im.V1.File.Create(ctx, larkim.NewCreateFileReqBuilder().Body(&larkim.CreateFileReqBody{FileType: &fileType, FileName: &fileName, File: file}).Build())
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
	message, err := s.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(target.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(target.ID).MsgType(larkim.MsgTypeFile).Content(string(content)).Build()).Build())
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
	upload, err := s.client.Im.V1.Image.Create(ctx, larkim.NewCreateImageReqBuilder().Body(larkim.NewCreateImageReqBodyBuilder().ImageType(imageType).Image(file).Build()).Build())
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
	message, err := s.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(target.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(target.ID).MsgType(larkim.MsgTypeImage).Content(string(content)).Build()).Build())
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
func (s *Sender) CreateDocumentAndAnnounce(ctx context.Context, target worker.ReplyTarget, title string, markdown []byte) (worker.DocumentOutcome, error) {
	if target.ID == "" || target.Type == "" || len(markdown) == 0 {
		return worker.DocumentOutcome{}, fmt.Errorf("document parameters are invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body := larkdocx.NewCreateDocumentReqBodyBuilder().Title(title)
	if s.defaultDocFolderToken != "" {
		body.FolderToken(s.defaultDocFolderToken)
	}
	request := larkdocx.NewCreateDocumentReqBuilder().Body(body.Build()).Build()
	response, err := s.client.Docx.V1.Document.Create(ctx, request)
	if err != nil {
		return worker.DocumentOutcome{}, fmt.Errorf("create document: %w", err)
	}
	if !response.Success() || response.Data == nil || response.Data.Document == nil || response.Data.Document.DocumentId == nil {
		return worker.DocumentOutcome{}, fmt.Errorf("create document: code=%d", response.Code)
	}
	url := "https://feishu.cn/docx/" + *response.Data.Document.DocumentId
	converted, convertErr := s.client.Docx.V1.Document.Convert(ctx, larkdocx.NewConvertDocumentReqBuilder().Body(larkdocx.NewConvertDocumentReqBodyBuilder().ContentType(larkdocx.ContentTypeMarkdown).Content(string(markdown)).Build()).Build())
	if convertErr != nil || converted == nil || !converted.Success() || converted.Data == nil || len(converted.Data.FirstLevelBlockIds) == 0 || len(converted.Data.Blocks) == 0 || len(converted.Data.Blocks) > maxDocumentDescendantBlocks {
		if _, announceErr := s.SendText(ctx, target.ID, target.Type, url); announceErr != nil {
			return worker.DocumentOutcome{}, fmt.Errorf("announce document after markdown conversion failure: %w", announceErr)
		}
		return worker.DocumentOutcome{URL: url, ContentWritten: false, AnnouncementOutcome: "sent"}, nil
	}
	sanitizeConvertedBlocks(converted.Data.Blocks)
	write := larkdocx.NewCreateDocumentBlockDescendantReqBuilder().DocumentId(*response.Data.Document.DocumentId).BlockId(*response.Data.Document.DocumentId).DocumentRevisionId(-1).Body(larkdocx.NewCreateDocumentBlockDescendantReqBodyBuilder().ChildrenId(converted.Data.FirstLevelBlockIds).Descendants(converted.Data.Blocks).Index(-1).Build()).Build()
	writeResponse, writeErr := s.client.Docx.V1.DocumentBlockDescendant.Create(ctx, write)
	if writeErr != nil || !writeResponse.Success() {
		if _, announceErr := s.SendText(ctx, target.ID, target.Type, url); announceErr != nil {
			return worker.DocumentOutcome{}, fmt.Errorf("announce partially written document: %w", announceErr)
		}
		return worker.DocumentOutcome{URL: url, ContentWritten: false, AnnouncementOutcome: "sent"}, nil
	}
	if _, err := s.SendText(ctx, target.ID, target.Type, url); err != nil {
		return worker.DocumentOutcome{URL: url, ContentWritten: true, AnnouncementOutcome: "unknown"}, nil
	}
	return worker.DocumentOutcome{URL: url, ContentWritten: true, AnnouncementOutcome: "sent"}, nil
}

// ReadDocument retrieves only the plain text for a document the current App
// can already access. Its caller owns delivery to the bound Codex Turn.
func (s *Sender) ReadDocument(ctx context.Context, documentID string) (string, error) {
	if documentID == "" {
		return "", fmt.Errorf("document id is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := s.client.Docx.V1.Document.RawContent(ctx, larkdocx.NewRawContentDocumentReqBuilder().DocumentId(documentID).Build())
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

func (s *Sender) CreateBatchCard(ctx context.Context, batch worker.Batch) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	content, err := RenderWorkCardJSON("*思考中…*", "等待 Codex 进展…", "生成中…", false)
	if err != nil {
		return "", fmt.Errorf("marshal batch card: %w", err)
	}
	if len(content) > maxCardPayloadBytes {
		return "", fmt.Errorf("batch card payload exceeds %d bytes", maxCardPayloadBytes)
	}
	var cardResp *larkcardkit.CreateCardResp
	if s.cardKitAvailable() {
		cardResp, err = s.client.Cardkit.V1.Card.Create(ctx, larkcardkit.NewCreateCardReqBuilder().Body(larkcardkit.NewCreateCardReqBodyBuilder().Type("card_json").Data(string(content)).Build()).Build())
	}
	if err == nil && cardResp != nil && cardResp.Success() && cardResp.Data != nil && cardResp.Data.CardId != nil {
		content, err = json.Marshal(map[string]any{"type": "card", "data": map[string]string{"card_id": *cardResp.Data.CardId}})
		if err != nil {
			return "", fmt.Errorf("marshal cardkit message: %w", err)
		}
	} else {
		content, err = RenderWorkCardJSON("*思考中…*", "等待 Codex 进展…", "生成中…", false)
		if err != nil {
			return "", fmt.Errorf("marshal patch fallback card: %w", err)
		}
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(batch.Messages[0].Reply.Type).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(batch.Messages[0].Reply.ID).MsgType(larkim.MsgTypeInteractive).Content(string(content)).Build()).Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
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
	slog.Info("feishu_card_created", "event", "feishu_card_created", "batch_id", batch.ID, "message_id", messageID, "mode", map[bool]string{true: "cardkit", false: "patch"}[cardResp != nil && cardResp.Success() && cardResp.Data != nil && cardResp.Data.CardId != nil])
	return messageID, nil
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
			slog.Warn("feishu_cardkit_fallback", "event", "feishu_cardkit_fallback", "message_id", messageID, "reason", stableCardKitReason(err), "operation", fields.operation, "code", fields.code)
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
	resp, err := s.client.Cardkit.V1.Card.Update(ctx, larkcardkit.NewUpdateCardReqBuilder().CardId(cardID).Body(larkcardkit.NewUpdateCardReqBodyBuilder().Card(card).Uuid(fmt.Sprintf("%s-%d", messageID, sequence)).Sequence(sequence).Build()).Build())
	if err != nil || !resp.Success() {
		s.dropCardSession(messageID)
		if err != nil {
			return err
		}
		return &cardKitRequestError{operation: "full_update", code: resp.Code}
	}
	slog.Info("feishu_card_updated", "event", "feishu_card_updated", "message_id", messageID, "mode", "cardkit_full", "closed", closed)
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
	slog.Debug("feishu_cardkit_session_retained", "event", "feishu_cardkit_session_retained", "message_id", messageID)
}

func (s *Sender) patchCard(ctx context.Context, messageID string, card []byte) error {
	if len(card) > maxCardPayloadBytes {
		return fmt.Errorf("patch payload exceeds %d bytes", maxCardPayloadBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req := larkim.NewPatchMessageReqBuilder().MessageId(messageID).Body(larkim.NewPatchMessageReqBodyBuilder().Content(string(card)).Build()).Build()
	resp, err := s.client.Im.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("update batch card: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("update batch card: code=%d", resp.Code)
	}
	slog.Info("feishu_card_updated", "event", "feishu_card_updated", "message_id", messageID, "mode", "patch")
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
	resp, err := s.client.Im.Message.Create(ctx, req)
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
