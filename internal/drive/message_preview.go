package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	ErrPreviewUnsupported    = errors.New("unsupported preview")
	ErrPreviewMessageDeleted = errors.New("message deleted")
	previewLinks             = regexp.MustCompile(`https://[A-Za-z0-9.-]+/(docx|minutes|base|file|wiki|sheets)/[A-Za-z0-9_-]+(?:[?#][^\s<>"'，。；！？）\x{3000}]*)?`)
)

// MessageReferences recognizes preview resources without changing the drive sync index.
func MessageReferences(typ, body string) []Ref {
	var value any
	if json.Unmarshal([]byte(body), &value) != nil {
		if typ != "text" {
			return nil
		}
		value = body
	}
	seen := map[[2]string]bool{}
	var refs []Ref
	add := func(kind, token, name, href string) {
		id := [2]string{kind, token}
		if !key.MatchString(token) || seen[id] {
			return
		}
		seen[id] = true
		if name == "" {
			name = href
		}
		refs = append(refs, Ref{Kind: kind, Token: token, Name: strings.ReplaceAll(name, "\x00", ""), URL: href})
	}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth >= 24 {
			return
		}
		switch node := value.(type) {
		case string:
			for _, match := range previewLinks.FindAllStringIndex(node, -1) {
				if match[0] > 0 && strings.ContainsRune("/@=", rune(node[match[0]-1])) ||
					match[1] < len(node) && strings.ContainsRune("/@%", rune(node[match[1]])) {
					continue
				}
				href := strings.TrimRight(node[match[0]:match[1]], ").,;]")
				u, err := url.Parse(href)
				if err != nil || u.User != nil {
					continue
				}
				host := strings.ToLower(u.Hostname())
				if host != "feishu.cn" && !strings.HasSuffix(host, ".feishu.cn") && host != "larksuite.com" && !strings.HasSuffix(host, ".larksuite.com") {
					continue
				}
				parts := strings.Split(u.Path, "/")
				add(parts[1], parts[2], "", href)
			}
		case []any:
			for _, child := range node {
				visit(child, depth+1)
			}
		case map[string]any:
			tag, _ := node["tag"].(string)
			if (depth == 0 && typ == "file") || tag == "file" {
				token, _ := node["file_key"].(string)
				name, _ := node["file_name"].(string)
				add("attachment", token, name, "")
			}
			if (depth == 0 && typ == "image") || tag == "img" || tag == "image" {
				token, _ := node["image_key"].(string)
				if token == "" {
					token, _ = node["img_key"].(string)
				}
				add("image", token, "图片", "")
			}
			fields := make([]string, 0, len(node))
			for field := range node {
				fields = append(fields, field)
			}
			sort.Strings(fields)
			for _, field := range fields {
				visit(node[field], depth+1)
			}
		}
	}
	visit(value, 0)
	return refs
}

type MessagePreviewResult struct {
	Stage string          `json:"stage"`
	Kind  string          `json:"kind,omitempty"`
	Name  string          `json:"name,omitempty"`
	Meta  json.RawMessage `json:"meta,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	PNG   []byte          `json:"png,omitempty"`
}

type MessagePreviewOptions = previewOptions

func MessagePreviewParams(r *http.Request) (MessagePreviewOptions, bool) { return previewParams(r) }

// RenderMessagePreview uses the same limits and converter as versioned drive files.
func RenderMessagePreview(ctx context.Context, options MessagePreviewOptions, ref Ref, load func() (Snapshot, error)) (MessagePreviewResult, error) {
	kind := ref.Kind
	if kind == "attachment" && ref.Name != "" && previewFileKind(ref.Name) == "" {
		return MessagePreviewResult{Stage: "unsupported"}, nil
	}
	if kind == "sheets" {
		snapshot, err := load()
		if err != nil {
			return MessagePreviewResult{}, err
		}
		return renderChatWorkbook(snapshot.Content, snapshot.Name, options)
	}
	select {
	case previewSlot <- struct{}{}:
		defer func() { <-previewSlot }()
	default:
		return MessagePreviewResult{Stage: "conversion_busy"}, nil
	}
	snapshot, err := load()
	if err != nil {
		return MessagePreviewResult{}, err
	}
	switch kind {
	case "docx":
		kind = "docs"
	case "file", "attachment":
		kind = previewFileKind(snapshot.Name)
		if kind == "" {
			if snapshot.Stream != nil {
				snapshot.Stream.Close()
			}
			return MessagePreviewResult{Stage: "unsupported"}, nil
		}
	}
	data, err := readPreviewBytes(snapshot, previewInputLimit(kind))
	if err != nil {
		return MessagePreviewResult{}, err
	}
	out := renderPreview(ctx, kind, data, options)
	result := MessagePreviewResult{Stage: out.Stage, Meta: out.Meta, Kind: kind, Name: snapshot.Name}
	switch out.MIME {
	case "image/png":
		result.PNG = out.Body
	case "application/json":
		if json.Valid(out.Body) {
			result.Data = out.Body
		}
	}
	return result, nil
}

func previewFileKind(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	switch ext {
	case "pdf", "docx", "xlsx", "pptx", "zip", "txt":
		return ext
	default:
		return ""
	}
}

func ReadPreviewBytes(s Snapshot) ([]byte, error) {
	return readPreviewBytes(s, previewFileLimit)
}

func readPreviewBytes(s Snapshot, limit int) ([]byte, error) {
	var reader io.Reader = bytes.NewReader(s.Content)
	if s.Stream != nil {
		defer s.Stream.Close()
		reader = s.Stream
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(data) > limit {
		return nil, ErrPartial
	}
	return data, nil
}
