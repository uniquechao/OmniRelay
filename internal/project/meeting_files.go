package project

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (k taskScope) meetingFiles(ctx context.Context, db projectReader) (map[string]bool, error) {
	rows, err := db.Query(ctx, "SELECT file_ref FROM project_meeting_files WHERE "+projectWhere, k.args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	marks := make(map[string]bool)
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		marks[ref] = true
	}
	return marks, rows.Err()
}

func (s *Service) checkMeetingFile(ctx context.Context, tx pgx.Tx, k taskScope, actor identity.Principal, ref string) error {
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || !identifier.MatchString(id) {
		return errInput
	}
	switch kind {
	case "document", "minutes":
		_, err := s.source(ctx, tx, k, actor, kind, id)
		return err
	case "drive":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 1 || strconv.FormatInt(n, 10) != id {
			return errInput
		}
		if s.files == nil {
			return errDenied
		}
		visible, err := s.files.CanMarkProjectFile(ctx, actor, k.project, n)
		if err != nil {
			return err
		}
		if !visible {
			return errDenied
		}
		return nil
	default:
		return errInput
	}
}

func (s *Service) markMeetingFile(ctx context.Context, k taskScope, actor identity.Principal, ref string) (any, error) {
	err := s.withMutation(ctx, func(tx pgx.Tx) error {
		allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.assign", k.project)
		if err != nil {
			return err
		}
		if !allowed {
			return errDenied
		}
		if err := s.checkMeetingFile(ctx, tx, k, actor, ref); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_meeting_files(tenant_key,app_id,project_id,file_ref,marked_by)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, k.args(ref, actor.AccountID)...)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return k.auditProfile(ctx, tx, actor, "project.meeting.mark", 1, map[string]string{"file_ref": ref})
	})
	return map[string]bool{"marked": true}, err
}

func (s *Service) markMeeting(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Ref string `json:"file_ref"`
	}
	if !decodeLimit(w, r, &in, 4096) {
		return nil, errInput
	}
	return s.markMeetingFile(r.Context(), scope(actor, r.PathValue("projectID")), actor, in.Ref)
}
