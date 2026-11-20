# Feishu Resignation Sync

## 职责边界

负责群聊人员的离职核实和展示。账号停用仍由既有通讯录名册流程管理。

## 关联源码路径

- `internal/directory/chat_senders.go` 核实历史群消息中的发送人，`internal/directory/people.go` 为已授权的群聊人员提供状态。
- `design/prototype/omni-relay-ui.js` 提供消息姓名、头像和成员列表共用的人员卡片。

## 长期设计约束

- 按租户、应用和飞书 open_id 核实身份。当前快照的人员状态优先。目录缺席、无权限、超时或限流本身均不能确认离职。
- 群聊补查结果与通讯录快照同事务发布。读取历史消息无需逐条访问飞书。

## 接口与测试

- 群聊人员返回可选布尔字段 `resigned`。回归入口为 `TestGroupSenderDeparture` 和 `tests/integration/chat_reader_browser.test.mjs`。

<!-- panda:module-meta:start -->
---
sync_uid: 01a1f9d4-01de-7d01-80ae-217564ed7e20
module_id: 78
project_id: 25
slug: feishu-resignation-sync
display_name: "Feishu Resignation Sync"
agent: codex
source: auto
status: active
created_ts: 1795192783326
last_used_ts: 1795192783614
---
<!-- panda:module-meta:end -->
