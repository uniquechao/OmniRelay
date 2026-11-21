# Overview Dashboard

## 职责边界

工作台总览 `#/workbench` 展示当前账号可访问的指标、项目速览与系统管理链接。各业务模块负责数据读取、可见范围与明细页面。

## 关联源码路径

- `frontend/src/features/workbench/Overview.tsx` 负责总览界面、统计请求和刷新状态。
- `design/prototype/omni-relay-workbench.html#/workbench` 提供标记为示例数据的交互原型，与正式页面共用 `design/prototype/omni-relay-overview.css`。

## 长期设计约束

- 指标和链接随会话导航权限显示，实际数据仍由各业务接口鉴权。
- 项目指标统计可访问项目，项目速览使用同一次请求的首批项目。消息统计当前有权查看、已归档且未撤回的消息，文件统计可见网盘文件，人员统计已启用的工作台账号。
- 四项统计独立加载。来源读取失败时显示暂不可用，真实空结果显示为 0。刷新会重新读取各项可见数据。

## 接口与测试

- 项目、文件与启用用户分别复用 `GET /api/v1/projects`、`GET /api/v1/feishu/files`、`GET /api/v1/system/user-page?limit=1&offset=0&status=active` 的 `total`。
- `GET /api/v1/feishu/chats/messages/count` 沿用聊天会话与消息可见规则，只返回 `total`。定向数据库验证由 `internal/chat/chat_test.go` 的 `TestChatReadAuthorizationAndPagination` 覆盖。

<!-- panda:module-meta:start -->
---
sync_uid: 01a1fa14-8c74-7b51-89f5-81ff1c21a0b2
module_id: 83
project_id: 25
slug: overview-dashboard
display_name: "Overview Dashboard"
agent: codex
source: auto
status: active
created_ts: 1795197013108
last_used_ts: 1795197013210
---
<!-- panda:module-meta:end -->
