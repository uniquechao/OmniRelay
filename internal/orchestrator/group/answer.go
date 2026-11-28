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
		var page struct {
			Chat  string `json:"chat_id"`
			Items []struct {
				ID, Text string
				Sent     string `json:"sent_at"`
			}
		}
		if json.Unmarshal(raw, &page) != nil || page.Chat == "" || page.Chat != args.String("chat_id") {
			return nil
		}
		var out []string
		for _, item := range page.Items {
			if item.Text != "" && item.ID != "" && item.Sent != "" {
				out = append(out, fmt.Sprintf("群 %s · 消息 %s · %s", page.Chat, item.ID, item.Sent))
			}
		}
		return out
	case "task_get":
		var task struct {
			ID      string
			Code    string `json:"task_code"`
			Version int64
		}
		if json.Unmarshal(raw, &task) == nil && task.ID != "" && task.ID == args.String("task_id") && task.Version > 0 {
			return []string{fmt.Sprintf("任务 %s（%s）· 版本 %d", task.Code, task.ID, task.Version)}
		}
	case "briefing_get":
		var brief struct {
			Date    string `json:"business_date"`
			Body    string
			Version int64
		}
		if json.Unmarshal(raw, &brief) == nil && brief.Body != "" && brief.Date == args.String("business_date") && brief.Version > 0 {
			return []string{fmt.Sprintf("项目简报 %s · 版本 %d", brief.Date, brief.Version)}
		}
	case "task_activity_list":
		var page struct {
			Items []struct {
				ID      int64
				Kind    string
				Created string `json:"created_at"`
			}
		}
		if json.Unmarshal(raw, &page) != nil {
			return nil
		}
		var out []string
		for _, event := range page.Items {
			if event.ID > 0 && event.Created != "" && (event.Kind == "comment" || event.Kind == "change") {
				out = append(out, fmt.Sprintf("任务 %s · 活动 %d（%s）· %s", args.String("task_id"), event.ID, event.Kind, event.Created))
			}
		}
		return out
	case "briefing_list":
		var page struct {
			Items []struct {
				Date    string `json:"business_date"`
				Version int64
			}
		}
		if json.Unmarshal(raw, &page) != nil {
			return nil
		}
		var out []string
		for _, b := range page.Items {
			if b.Date != "" && b.Version > 0 {
				out = append(out, fmt.Sprintf("已保存简报目录 %s · 版本 %d（摘要）", b.Date, b.Version))
			}
		}
		return out
	case "daily_report_query":
		var out []string
		if args.String("source") == "published" {
			var page struct {
				Items []struct {
					Date    string `json:"business_date"`
					Chat    string `json:"chat_id"`
					Message string `json:"message_id"`
				}
			}
			if json.Unmarshal(raw, &page) == nil {
				for _, r := range page.Items {
					if r.Date != "" && r.Chat != "" && r.Message != "" {
						out = append(out, fmt.Sprintf("已发布日报统计 %s · 群 %s · 消息 %s", r.Date, r.Chat, r.Message))
					}
				}
			}
		} else {
			var page struct {
				Data struct {
					Items []struct {
						Date    string `json:"work_date"`
						Account string `json:"account_id"`
						Note    string
						Version int64
					}
				}
			}
			if json.Unmarshal(raw, &page) == nil {
				for _, r := range page.Data.Items {
					if r.Note != "" && r.Account != "" && r.Date != "" && r.Version > 0 {
						out = append(out, fmt.Sprintf("成员日报 %s · %s · 版本 %d", r.Date, r.Account, r.Version))
					}
				}
			}
		}
		return out
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
