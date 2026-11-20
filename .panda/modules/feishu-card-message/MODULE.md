# Feishu Card Message

## 职责边界

负责工作台机器人记录和项目关联群中的飞书历史卡片及消息资源预览。会话读取沿用各入口的授权接口。

## 关联源码路径

- `web/chat-card.js` 解析和渲染 `interactive` 消息。
- `web/chat-reader.js` 和 `web/chat-reader.css` 接入共用消息时间线并应用主题样式。
- `web/chat-preview.js` 接入右侧预览抽屉、图片放大和飞书授权回跳。
- `internal/chat/message_preview.go` 核验原消息及资源，`internal/feishu/message_preview.go` 解析知识库节点和读取电子表格。

## 长期设计约束

- 消息接口的 `body` 保留飞书结构。支持历史接口的 `title` 与分行 `elements`，以及卡片中的 `header`、`elements` 或 `body.elements`。
- 卡片按数据中的顺序显示正文、操作与说明。标题色优先使用已识别的模板主题，缺省采用工作台信息色。
- 工作台历史按钮需要有效的 HTTP 或 HTTPS 地址。支持的飞书资源链接打开预览抽屉，其他链接沿用原导航。仅有标签、回调或无效地址的按钮以禁用态显示。撤回消息沿用阅读器的撤回提示。
- 文件与图片按原会话、消息和资源键读取。聊天文件复用网盘转换与展示能力，直接读取消息附件。图片同源返回受支持的栅格格式。
- 飞书文档按当前用户的飞书权限读取。预览授权独立于登录，短时凭证按工作台会话和资源类型保留在服务端内存中。

## 接口与测试

- `tests/integration/chat_reader_browser.test.mjs` 覆盖历史卡片分层、浅深主题、宽窄屏幕、键盘链接、异常回退、消息搜索和项目入口复用。
- 消息预览回归覆盖机器人记录和项目关联群、源消息撤回、伪造资源键、会话隔离和飞书授权回跳。

<!-- panda:module-meta:start -->
---
sync_uid: 01a1fa03-105b-7d97-8949-3063311bb87d
module_id: 80
project_id: 25
slug: feishu-card-message
display_name: "Feishu Card Message"
agent: codex
source: auto
status: active
created_ts: 1795195867227
last_used_ts: 1795195867292
---
<!-- panda:module-meta:end -->
