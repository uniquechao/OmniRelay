package chat

import (
	"encoding/json"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var paragraphStart = regexp.MustCompile(`(?i)^<p(?:\s|>)`)

// Some archived text messages contain editor paragraphs instead of plain text.
// Extract text only inside that envelope. Ordinary examples of markup stay literal.
func paragraphText(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 7 || !strings.EqualFold(trimmed[len(trimmed)-4:], "</p>") || !paragraphStart.MatchString(trimmed) {
		return value
	}
	context := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(trimmed), context)
	if err != nil {
		return value
	}
	var out strings.Builder
	var writeText func(*html.Node)
	writeText = func(node *html.Node) {
		switch node.DataAtom {
		case atom.Script, atom.Style, atom.Template:
			return
		case atom.Br:
			out.WriteByte('\n')
		}
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			writeText(child)
		}
	}
	firstParagraph := true
	for _, node := range nodes {
		if node.Type == html.TextNode && strings.TrimSpace(node.Data) == "" {
			continue
		}
		if node.DataAtom == atom.P {
			if !firstParagraph {
				out.WriteByte('\n')
			}
			firstParagraph = false
		}
		writeText(node)
	}
	return out.String()
}

var mentionToken = regexp.MustCompile(`@_user_[0-9]+|@_all\b`)

func resolveMentionText(text string, names map[string]string) string {
	if !strings.Contains(text, "@_") {
		return text
	}
	return mentionToken.ReplaceAllStringFunc(text, func(key string) string {
		if name := names[key]; name != "" {
			return "@" + name
		}
		if key == "@_all" {
			return "@所有人"
		}
		return "@成员"
	})
}

func searchableText(kind, body string, mentions map[string]string) string {
	var content any
	if json.Unmarshal([]byte(body), &content) != nil {
		if kind == "text" {
			return resolveMentionText(paragraphText(body), mentions)
		}
		return ""
	}
	var parts []string
	var collect func(any)
	collect = func(node any) {
		switch node := node.(type) {
		case []any:
			for _, child := range node {
				collect(child)
			}
		case map[string]any:
			if kind == "post" && node["tag"] == "at" {
				id, _ := node["user_id"].(string)
				name, _ := node["user_name"].(string)
				switch {
				case id == "all":
					name = "所有人"
				case mentions[id] != "":
					name = mentions[id]
				case name == "":
					name = "成员"
				}
				parts = append(parts, "@"+name)
				return
			}
			for _, key := range []string{"text", "title", "content", "file_name", "name", "elements", "header"} {
				collect(node[key])
			}
			// Rich text can wrap its elements in a language key.
			for _, key := range []string{"zh_cn", "en_us", "ja_jp"} {
				collect(node[key])
			}
		case string:
			if kind == "text" || kind == "post" {
				node = resolveMentionText(paragraphText(node), mentions)
			}
			parts = append(parts, node)
		}
	}
	collect(content)
	return strings.Join(parts, " ")
}
