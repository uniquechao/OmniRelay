# 后端运行说明

OmniRelay Go 服务提供飞书登录、用户与角色管理、审计，以及飞书部门和人员同步。下文以单个飞书组织和单套服务为例。

## 启动

1. 使用 Go 1.26.11 或更新的安全补丁版本构建服务。准备 PostgreSQL 和客户端信任的数据库 CA。数据库地址使用 `sslmode=verify-full`，证书覆盖连接地址的主机名。
2. 创建飞书自建应用，将 `https://<工作台域名>/auth/feishu/callback` 添加到 OAuth 重定向 URL。在飞书应用后台批准需要同步的部门和人员范围，以及读取这些部门和人员所需的通讯录权限。单独授权指定人员时，核对获取通讯录授权范围所需的用户 ID 权限。 [飞书登录说明](https://open.feishu.cn/document/client-docs/build-login-free-system-) · [获取通讯录授权范围](https://open.feishu.cn/document/server-docs/contact-v3/scope/list)
3. 参考 [环境变量示例](../config/backend.env.example)，将应用密钥、数据库凭据和 CA 交给部署环境。设置 `OMNIRELAY_BOOTSTRAP_OPEN_ID` 为通过同一飞书应用核验过的首位管理员 Open ID。初始化记录持久化后即可移除该变量。
4. 先用 schema 所有者账号执行 `migrate`，再用受限账号启动 `serve`。运行账号需要目标 schema 的 USAGE、业务表读写权限、同步任务和审计序列的 USAGE，以及迁移记录的 SELECT 权限。迁移程序会核对历史文件校验值。

```sh
go build -o ./bin/omnirelay ./cmd/omnirelay

# 在迁移 Job 的环境中运行。
./bin/omnirelay migrate

# 在 API 进程的环境中运行。
./bin/omnirelay serve
```

HTTPS 入口转发 `/auth/`、`/api/` 与探针路径到 Go 服务，将工作台页面部署在同一来源。入口日志只记录登录请求路径，省略 OAuth 查询参数。Kubernetes 可用 `GET /healthz` 检查进程，`GET /readyz` 检查数据库和迁移状态。

## 账号开通与权限

管理员从 `/auth/feishu/start` 登录，回调验证通过后获取服务端会话。浏览器调用 `GET /api/v1/me` 获取当前主体和 CSRF 值。创建账号、修改角色、同步通讯录和退出等写操作需要同源 `Origin`、会话 Cookie 及 `X-CSRF-Token`。

| 操作 | 入口 |
| --- | --- |
| 开通账号 | `POST /api/v1/system/accounts`，请求体含本应用的 `openId`，可选 `name` |
| 列出、启用或停用账号 | `GET /api/v1/system/accounts`、`PATCH /api/v1/system/accounts/{accountID}`，更新请求体为 `{"active":false}` 或 `{"active":true}` |
| 查询、授予或撤销平台角色 | `GET` / `POST /api/v1/system/role-grants`、`DELETE /api/v1/system/role-grants/{grantID}`，授权请求体含 `accountId` 和 `role` |
| 查看审计 | `GET /api/v1/system/audit-events` |
| 退出当前会话 | `POST /auth/logout` |

首次登录需要先开通账号。通讯录可帮助管理员确认人员及其部门。登录以同应用 Open ID 和租户标识匹配账号。角色标识为 `system_admin`、`system_operator` 和 `audit_viewer`。服务会保留至少一位有效管理员。撤销角色或停用账号后，受影响的会话立即失效。项目权限以 OmniGrid 正式成员关系为准，接入正式成员解析器前服务拒绝项目鉴权。

## 组织同步

服务启动后会检查是否需要同步，默认每小时刷新一次。可通过 `OMNIRELAY_DIRECTORY_INTERVAL` 调整刷新周期。系统管理员可调用 `POST /api/v1/system/directory/sync-runs` 立即触发，同源已有任务时返回该任务。任务历史和详情分别由 `GET /api/v1/system/directory/sync-runs` 及 `GET /api/v1/system/directory/sync-runs/{syncID}` 提供。

- `GET /api/v1/system/directory/departments` 按 `parent_id` 查询子部门。
- `GET /api/v1/system/directory/people` 支持按 `department_id`、`include_descendants`、`q` 和 `account_status` 筛选。同一人员的多个部门归属会随结果返回。
- 翻页可传回上次响应的 `snapshot_version`，若期间发布了新版本，服务返回 409，客户端重新从第一页读取。
- 目录同步收到授予范围被撤销的响应时会立即隐藏旧目录。变更由定时或手动同步发现。暂停或离职人员的 OmniRelay 会话可通过停用账号立即撤销。

## 验证

```sh
go test ./...
go vet ./...
```

为运行 PostgreSQL 集成测试，将 `OMNIRELAY_TEST_DATABASE_URL` 和 `OMNIRELAY_IDENTITY_TEST_DSN` 指向专用测试库。测试会创建并清理隔离的 schema，并用模拟服务执行飞书授权及目录调用。
