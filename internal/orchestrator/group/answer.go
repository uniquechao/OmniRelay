package group

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
)

// Citations are derived from material actually returned to this turn's tools.
// Metadata-only hits cannot establish what a document says.
func evidenceCitations(name string, args pmtools.Args, raw json.RawMessage) []string {
	switch name {
	case "file_get":
		var f struct {
			State, Revision string
			Page            int
			File            struct {
				Title     string
				VersionID int64 `json:"version_id"`
			}
			Body struct{ Page int }
		}
		if json.Unmarshal(raw, &f) != nil || f.State != "ready" {
			return nil
		}
		if f.Page == 0 {
			f.Page = f.Body.Page
		}
		if f.Revision == "" {
			f.Revision = fmt.Sprint(f.File.VersionID)
		}
		return []string{fmt.Sprintf("%s（%s，第 %d 页，版本 %s）", f.File.Title, args.String("file_ref"), f.Page, f.Revision)}
	case "chat_search_messages":
		var page struct{ Items []struct{ ID, Text string } }
		if json.Unmarshal(raw, &page) != nil {
			return nil
		}
		var out []string
		for _, item := range page.Items {
			if item.Text != "" && item.ID != "" {
				out = append(out, fmt.Sprintf("群 %s · 消息 %s", args.String("chat_id"), item.ID))
			}
		}
		return out
	case "task_get":
		return []string{"任务 " + args.String("task_id")}
	case "briefing_get":
		var brief struct{ Version int64 }
		if json.Unmarshal(raw, &brief) == nil && brief.Version > 0 {
			return []string{fmt.Sprintf("项目简报 %s · 版本 %d", args.String("business_date"), brief.Version)}
		}
	}
	return nil
}

func citedAnswer(ctx context.Context, tx pgx.Tx, w work, text string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT unnest(citations) AS ref FROM agent_project_tool_reads WHERE work_id=$1 ORDER BY ref`, w.Work)
	if err != nil {
		return "", err
	}
	cites, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	if len(cites) == 0 {
		return "现有可读资料不足以回答这个问题。请指定相关纪要、文档、群消息或任务。", nil
	}
	return text + "\n\n本轮查阅的依据\n" + strings.Join(cites, "\n"), nil
}
