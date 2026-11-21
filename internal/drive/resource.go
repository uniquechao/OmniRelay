package drive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRevoked         = errors.New("file access revoked")
	ErrAppScopeMissing = errors.Join(ErrRevoked, errors.New("file application scope missing"))
	ErrPartial         = errors.New("partial file")
	ErrUnavailable     = errors.New("file source unavailable")
)

type RetryAfterError struct{ Delay time.Duration }

func (e *RetryAfterError) Error() string { return "飞书请求受到限流" }
func (e *RetryAfterError) Unwrap() error { return ErrUnavailable }

type Ref struct {
	Kind, Token, Name, MessageID, ChatKind, URL string
	Size                                        *int64
}
type Snapshot struct {
	Name    string
	Content []byte
	Stream  io.ReadCloser
}
type Source interface {
	ReadFile(context.Context, Ref, bool) (Snapshot, error)
}

var link = regexp.MustCompile(`https://(?:[A-Za-z0-9-]+\.)*(?:feishu\.cn|larksuite\.com)/(docx|wiki|minutes|base|file)/([A-Za-z0-9_-]+)\b`)
var key = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func References(typ, body string) []Ref {
	var v any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil {
		return nil
	}
	seen := map[[2]string]bool{}
	var out []Ref
	add := func(kind, token, name string, size *int64) {
		k := [2]string{kind, token}
		if !key.MatchString(token) || seen[k] {
			return
		}
		seen[k] = true
		if name == "" {
			name = token
		}
		out = append(out, Ref{Kind: kind, Token: token, Size: size, Name: strings.ReplaceAll(name, "\x00", "")})
	}
	var visit func(any)
	visit = func(v any) {
		switch value := v.(type) {
		case string:
			for _, m := range link.FindAllStringSubmatch(value, -1) {
				add(m[1], m[2], "", nil)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			token, _ := value["file_key"].(string)
			name, _ := value["file_name"].(string)
			tag, _ := value["tag"].(string)
			imageKey, _ := value["image_key"].(string)
			if typ == "image" || tag == "img" || tag == "image" {
				add("image", imageKey, "群聊图片", nil)
			}
			if typ == "media" || tag == "media" || tag == "video" {
				if name == "" {
					name = "群聊视频"
				}
				add("video", token, name, nil)
			}
			if typ == "file" || tag == "file" {
				var size *int64
				for _, field := range []string{"file_size", "size"} {
					var raw string
					switch n := value[field].(type) {
					case json.Number:
						raw = n.String()
					case string:
						raw = n
					}
					if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
						size = &n
						break
					}
				}
				add("attachment", token, name, size)
			}
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(v)
	return out
}

type FileFormat struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
}

var formats = map[string]FileFormat{
	"image": {"image", "图片", "files"}, "video": {"video", "视频", "files"},
	"docs": {"docs", "飞书文档", "docs"}, "minutes": {"minutes", "飞书妙记", "minutes"}, "base": {"base", "多维表格", "tasks"},
	"pdf": {"pdf", "PDF", "files"}, "word": {"word", "Word", "files"}, "excel": {"excel", "Excel", "files"}, "slides": {"slides", "PowerPoint", "files"}, "zip": {"zip", "ZIP", "files"}, "generic": {"generic", "其他文件", "files"},
}
var extensions = map[string]string{".jpg": "image", ".jpeg": "image", ".png": "image", ".gif": "image", ".webp": "image", ".mp4": "video", ".webm": "video", ".mov": "video", ".pdf": "pdf", ".doc": "word", ".docx": "word", ".xls": "excel", ".xlsx": "excel", ".ppt": "slides", ".pptx": "slides", ".zip": "zip"}

func Format(kind, name string) FileFormat {
	if kind == "docx" {
		kind = "docs"
	}
	if kind == "file" || kind == "attachment" {
		kind = extensions[strings.ToLower(path.Ext(name))]
	}
	if f, ok := formats[kind]; ok {
		return f
	}
	return formats["generic"]
}

func messageResource(kind string) bool {
	return kind == "attachment" || kind == "image" || kind == "video"
}
