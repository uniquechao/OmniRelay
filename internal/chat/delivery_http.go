package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type deliveryAuth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)

func (s *Service) registerDelivery(mux *http.ServeMux, auth deliveryAuth) {
	protect := func(next func(http.ResponseWriter, *http.Request, identity.Principal, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			policy := identity.ValidateSession
			if r.Method == http.MethodPost {
				policy = identity.RefreshSessionWithCSRF
			}
			actor, ok := auth(w, r, policy)
			if !ok {
				return
			}
			target := r.PathValue("chatID")
			if !validIdentifier(target) || !strings.HasPrefix(target, "oc_") {
				deliveryHTTPError(w, errDeliveryTarget)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			next(w, r.WithContext(ctx), actor, target)
		}
	}
	base := route + "/{chatID}"
	mux.HandleFunc("GET "+base+"/send-capabilities", protect(s.getSendCapabilities))
	mux.HandleFunc("POST "+base+"/attachments", protect(s.postAttachment))
	mux.HandleFunc("POST "+base+"/messages", protect(s.postDelivery))
	mux.HandleFunc("GET "+base+"/deliveries/{operationKey}", protect(s.getDelivery))
}

func (s *Service) getSendCapabilities(w http.ResponseWriter, r *http.Request, p identity.Principal, target string) {
	if err := s.deliveryAccount(r.Context(), p); err != nil {
		deliveryHTTPError(w, err)
		return
	}
	if !s.deliveryEnabled || s.delivery == nil {
		writeJSON(w, http.StatusOK, map[string]any{"can_send": false, "reason": "send_disabled"})
		return
	}
	c, err := s.deliveryTarget(r.Context(), p, target, nil, false)
	if err != nil {
		deliveryHTTPError(w, err)
		return
	}
	bot, err := s.source.Bot(r.Context())
	if err != nil {
		deliveryHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"can_send": true, "sender": DeliverySender{Kind: "bot", OpenID: bot.OpenID, Name: bot.Name},
		"formats": []string{"text", "post"}, "mentions": c.Kind == orchestrator.ChatGroup,
		"attachment_kinds": []string{"file", "image"}, "max_attachments": maxAttachments,
		"max_file_bytes": maxFileBytes, "max_image_bytes": maxImageBytes,
	})
}

func (s *Service) postDelivery(w http.ResponseWriter, r *http.Request, p identity.Principal, target string) {
	var input struct {
		OperationKey string `json:"operation_key"`
		DeliveryContent
	}
	reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	reader.DisallowUnknownFields()
	var extra json.RawMessage
	if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media != "application/json" ||
		reader.Decode(&input) != nil || !errors.Is(reader.Decode(&extra), io.EOF) {
		deliveryHTTPError(w, errDeliveryInput)
		return
	}
	result, err := s.send(r.Context(), p, target, input.OperationKey, input.DeliveryContent, nil, nil)
	if err != nil {
		deliveryHTTPError(w, err)
		return
	}
	status := http.StatusOK
	if result.State == "pending" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func (s *Service) getDelivery(w http.ResponseWriter, r *http.Request, p identity.Principal, target string) {
	operation := r.PathValue("operationKey")
	if !validIdentifier(operation) || len(operation) > 100 {
		deliveryHTTPError(w, errDeliveryMissing)
		return
	}
	result, err := s.deliveryStatus(r.Context(), p, target, operation, false)
	if err != nil {
		deliveryHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) postAttachment(w http.ResponseWriter, r *http.Request, p identity.Principal, target string) {
	ctx := r.Context()
	if !s.deliveryEnabled || s.delivery == nil {
		deliveryHTTPError(w, errDeliveryDisabled)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "file"
	}
	limit := maxFileBytes
	switch kind {
	case "image":
		limit = maxImageBytes
	case "file":
	default:
		deliveryHTTPError(w, errDeliveryInput)
		return
	}
	if _, err := s.deliveryTarget(ctx, p, target, nil, false); err != nil {
		deliveryHTTPError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit)+8192)
	reader, err := r.MultipartReader()
	if err != nil {
		deliveryHTTPError(w, errDeliveryInput)
		return
	}
	name, data, err := readDeliveryFile(reader, limit)
	if err != nil {
		deliveryHTTPError(w, err)
		return
	}
	if kind == "image" && !strings.HasPrefix(http.DetectContentType(data), "image/") {
		deliveryHTTPError(w, ErrAttachmentFailed)
		return
	}
	if _, err := s.source.Bot(ctx); err != nil {
		deliveryHTTPError(w, err)
		return
	}
	if _, err := s.deliveryTarget(ctx, p, target, nil, false); err != nil {
		deliveryHTTPError(w, err)
		return
	}
	resource, err := s.delivery.Upload(ctx, kind, name, data)
	if err != nil || resource == "" {
		deliveryHTTPError(w, ErrAttachmentFailed)
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		deliveryHTTPError(w, err)
		return
	}
	id := hex.EncodeToString(random[:])
	digest := sha256.Sum256(data)
	var expires time.Time
	err = s.pool.QueryRow(ctx, `INSERT INTO feishu_send_uploads
		(id,tenant_key,app_id,account_id,chat_id,generation,filename,kind,content_digest,resource_key)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10 FROM accounts a JOIN feishu_bot_configs b USING (tenant_key,app_id)
		WHERE (a.tenant_key,a.app_id,a.id,a.open_id)=($2,$3,$4,$11) AND a.active
		AND b.enabled AND b.credentials_generation=$6 RETURNING expires_at`,
		id, s.tenant, s.app, p.AccountID, target, s.generation, name, kind, digest[:], resource, p.OpenID).Scan(&expires)
	if err != nil {
		deliveryHTTPError(w, ErrAttachmentFailed)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "kind": kind, "name": name, "expires_at": expires})
}

func readDeliveryFile(reader *multipart.Reader, limit int) (string, []byte, error) {
	part, err := reader.NextPart()
	if err != nil {
		return "", nil, ErrAttachmentFailed
	}
	name := part.FileName()
	if part.FormName() != "file" || strings.TrimSpace(name) == "" || len(name) > 255 ||
		!validDeliveryText(name) || strings.ContainsAny(name, "\r\n/\\") || name == "." || name == ".." {
		return "", nil, ErrAttachmentFailed
	}
	data, err := io.ReadAll(io.LimitReader(part, int64(limit)+1))
	if err != nil || len(data) == 0 || len(data) > limit {
		return "", nil, ErrAttachmentFailed
	}
	if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		return "", nil, ErrAttachmentFailed
	}
	return name, data, nil
}

func deliveryHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDeliveryInput):
		writeError(w, http.StatusBadRequest, "invalid_delivery")
	case errors.Is(err, errDeliveryConflict):
		writeError(w, http.StatusConflict, "operation_conflict")
	case errors.Is(err, errDeliveryMissing):
		writeError(w, http.StatusNotFound, "operation_not_found")
	case errors.Is(err, errDeliveryTarget), errors.Is(err, ErrAccessLost), errors.Is(err, errHidden):
		writeError(w, http.StatusNotFound, deliveryReason(err))
	case errors.Is(err, errDeliveryDisabled):
		writeError(w, http.StatusServiceUnavailable, "send_disabled")
	case errors.Is(err, ErrAttachmentFailed):
		writeError(w, http.StatusUnprocessableEntity, "attachment_failed")
	default:
		writeError(w, http.StatusServiceUnavailable, "delivery_unavailable")
	}
}
