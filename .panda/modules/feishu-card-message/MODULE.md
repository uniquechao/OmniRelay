# Feishu Card Message

## 职责边界

负责工作台机器人记录和项目关联群中的飞书消息展示、资源预览及机器人发送。会话读取沿用各入口的授权接口。

## 关联源码路径

- `web/chat-card.js` 解析和渲染 `interactive` 消息。
- `web/chat-reader.js` 和 `web/chat-reader.css` 接入共用消息时间线并应用主题样式。
- `web/chat-preview.js` 渲染独立飞书文档卡片、右侧预览抽屉和图片放大。
- `internal/chat/message_preview.go` 核验原消息及资源。`internal/chat/document_sync.go` 负责文档快照队列，`internal/feishu/message_preview.go` 使用机器人身份解析知识库节点和同步电子表格。
- `web/chat-composer.js` 提供两个入口共用的发送框。`internal/chat/delivery.go` 负责投递授权和回执，`internal/feishu/delivery.go` 调用飞书发送接口。

## 长期设计约束

- 消息接口的 `body` 保留飞书结构。支持历史接口的 `title` 与分行 `elements`，以及卡片中的 `header`、`elements` 或 `body.elements`。
- 卡片按数据中的顺序显示正文、操作与说明。标题色优先使用已识别的模板主题，缺省采用工作台信息色。
- 工作台历史按钮需要有效的 HTTP 或 HTTPS 地址。支持的飞书资源链接打开预览抽屉，其他链接沿用原导航。仅有标签、回调或无效地址的按钮以禁用态显示。撤回消息沿用阅读器的撤回提示。
- 文件与图片按原会话、消息和资源键读取。聊天文件复用网盘转换与展示能力，直接读取消息附件。图片同源返回受支持的栅格格式。
- 聊天文档按原消息可见范围共享。机器人提前同步标题、实际类型和正文；私聊、普通群和项目关联群共用按原消息绑定的快照。链接编辑或消息撤回使旧快照失效，提供预览前须复核会话和原消息。文件网盘的项目范围独立维护。
- 标准 Kubernetes 发布向 Go Pod 注入机器人发送开关，可按部署显式关闭。发送仍由服务端核验机器人配置和会话权限。群聊通过候选选择生成结构化 @，发送前复核当前成员资格，未选择的 `@姓名` 保留为正文。未知投递结果沿用原操作键核对。
- 发送框的提及高亮和发送载荷共用草稿中的成员区间。输入 @ 或 ＠ 时，候选列表靠近触发字符；从工具栏打开时，列表靠近提及按钮。
- 网盘列表的来源按原消息路由去重。同一个群可以有多条原消息，每个定位图标对应一条；详情抽屉保留分享人和时间。

## 接口与测试

- `tests/integration/chat_reader_browser.test.mjs` 覆盖历史卡片分层、浅深主题、宽窄屏幕、键盘链接、异常回退、消息搜索和项目入口复用。
- 消息预览回归覆盖机器人记录和项目关联群、源消息撤回、伪造资源键、私聊隔离、机器人读取失败及同步中撤回。
- 发送回归覆盖群成员分页、编辑器 @ 选择、私聊限制、附件及原操作对账。`internal/chat/delivery_test.go` 覆盖发送授权及持久回执。

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
