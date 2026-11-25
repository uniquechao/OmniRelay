# User Management

## 职责边界

管理飞书通讯录人员的本地账号开通、启停及角色展示。身份模块负责验证飞书身份、签发会话和校验登录状态。

## 关联源码路径

- `frontend/src/features/users/UsersPage.tsx` 和 `admin.ts` 提供角色权限与用户管理界面，`frontend/src/api/system-api.ts` 对接并校验系统接口。
- `internal/access/admin.go` 和 `internal/access/user_page.go` 负责账号写入与用户列表读取。
- `internal/directory/feishu.go` 和 `internal/directory/service.go` 负责飞书状态识别和快照发布。

## 长期设计约束

- 组织内的独立管理员通过部署命令 `create-local-admin` 创建。其 `accounts.open_id IS NULL`，凭据、系统角色、会话和审计沿用工作台现有实现。飞书身份和目录匹配只接受实际 Open ID。

- 角色授予按目录返回的角色定义和目标会籍的 generation 提交。generation 是服务端签发的字符串，同一账号重新加入项目后，旧会籍的提交会被拒绝。

- 通讯录同步只更新目录。管理员通过「标记为普通用户」开通本地账号。启用账号自动获得 `system_user`，飞书登录要求身份匹配已启用的本地账号。
- 当前可见通讯录中的在职人员和明确未激活人员可开通。冻结、退出、未加入、离职及未知状态不具备开通资格。通讯录快照以 `unactivated` 补充 `directory_status`，既有合并状态需在下一次同步后重新识别。

## 接口与测试

- 个人 API Key 经浏览器会话在 `/api/v1/me/api-keys` 管理，业务 API 可通过 HTTPS Bearer 认证。身份从密钥所属账号解析，授权仍逐请求检查。数据库保留密钥摘要，账号停用会永久撤销原密钥。
- 个人 Harness 通过 `cmd/omnirelaycli` 使用所属用户的密钥调用业务 API。向项目 Bot 发问也沿用这个用户的身份，项目 PM 负责执行和署名。
- `GET /api/v1/system/user-page` 提供目录与本地账号状态，`POST /api/v1/system/accounts` 执行开通并写入审计。
- `TestBackendFeishuAdmissionAndRevocation` 覆盖从目录同步、拒绝未开通人员到显式开通后的飞书登录。

<!-- panda:module-meta:start -->
---
sync_uid: 01a1fa0b-fcad-750e-8d0e-68ea608e6af0
module_id: 82
project_id: 25
slug: user-management
display_name: "User Management"
agent: codex
source: auto
status: active
created_ts: 1795196452013
last_used_ts: 1795196452099
---
<!-- panda:module-meta:end -->
