package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

const taskFileLimit = 20 << 20
const taskBatchLimit = 32 << 20

var attachmentID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type taskUpload struct {
	ID, Name, Digest string
	content          []byte
}

func decodeTask(w http.ResponseWriter, r *http.Request, in *saveTask) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "multipart/form-data" {
		return decodeLimit(w, r, in, 96<<10)
	}
	r.Body = http.MaxBytesReader(w, r.Body, taskBatchLimit+(256<<10))
	parts, err := r.MultipartReader()
	if err != nil {
		return false
	}
	var fields bool
	var total int
	seen := map[string]bool{}
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		if part.FormName() == "fields" && part.FileName() == "" && !fields {
			raw, err := io.ReadAll(io.LimitReader(part, (96<<10)+1))
			if err != nil || len(raw) > 96<<10 {
				return false
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if dec.Decode(in) != nil || dec.Decode(new(any)) != io.EOF {
				return false
			}
			fields = true
			continue
		}
		id := part.FormName()
		name := part.FileName()
		if !attachmentID.MatchString(id) || seen[id] || name == "" || strings.ContainsRune(name, 0) || !textLimit(name, 255) || len(seen) >= 32 {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(part, taskFileLimit+1))
		if err != nil || len(data) > taskFileLimit {
			return false
		}
		total += len(data)
		if total > taskBatchLimit {
			return false
		}
		digest := sha256.Sum256(data)
		in.uploads = append(in.uploads, taskUpload{ID: id, Name: name, Digest: hex.EncodeToString(digest[:]), content: data})
		seen[id] = true
	}
	return fields && len(seen) > 0
}

func (k taskScope) storeTaskUploads(ctx context.Context, tx pgx.Tx, taskID string, in saveTask) error {
	for _, upload := range in.uploads {
		url := "/api/v1/projects/" + k.project + "/task-attachments/" + upload.ID
		if !strings.Contains(in.Description, "]("+url+")") && !strings.Contains(in.Acceptance, "]("+url+")") {
			return errInput
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_task_attachments(tenant_key,app_id,project_id,task_id,id,name,content_type,content)
     VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, k.args(taskID, upload.ID, upload.Name, http.DetectContentType(upload.content), upload.content)...)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) taskAttachment(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	k := scope(a, r.PathValue("projectID"))
	id := r.PathValue("attachmentID")
	if !attachmentID.MatchString(id) {
		return nil, errInput
	}
	var name, kind string
	var data []byte
	err := s.reader().QueryRow(r.Context(), `SELECT f.name,f.content_type,f.content FROM project_task_attachments f
 JOIN project_tasks t ON (t.tenant_key,t.app_id,t.project_id,t.id)=(f.tenant_key,f.app_id,f.project_id,f.task_id)
 WHERE (f.tenant_key,f.app_id,f.project_id,f.id)=($1,$2,$3,$4) AND t.deleted_at IS NULL`, k.args(id)...).Scan(&name, &kind, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errDenied
	}
	if err != nil {
		return nil, err
	}
	if err = s.require(r.Context(), a, "project.read", k.project); err != nil {
		return nil, err
	}
	disposition := "attachment"
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		disposition = "inline"
	default:
		kind = "application/octet-stream"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return streamedResponse{}, nil
}

type streamedResponse struct{}
