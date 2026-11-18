# Kubernetes 部署目录

资源拆分、连接、迁移、密钥与备份设计见 [OmniRelay Kubernetes 部署设计](../../design/specs/omni-relay-k8s-deployment-design.md)。

- `base/` 放 Go Deployment、Web 和工具 Service、DSH StatefulSet 与 Service、选主和网络资源，以及独立迁移 Job。
- `overlays/staging/` 放测试飞书应用所用的入口、配置及密钥引用。
- `overlays/production/` 放生产入口、资源和存储配置，以及生产密钥引用。

目录当前用于部署文件归位。清单随阶段零验证补齐。

## 飞书 Bot Lease

在 Go 副本所在命名空间预先应用 `base/feishu-bot-lease.yaml`。每个副本使用同一个 Lease，ServiceAccount 仅可读取和更新这个 Lease。

```sh
kubectl -n "$NAMESPACE" apply -f deploy/k8s/base/feishu-bot-lease.yaml
kubectl -n "$NAMESPACE" auth can-i update leases.coordination.k8s.io/omnirelay-feishu-bot \
  --as="system:serviceaccount:$NAMESPACE:omnirelay-feishu-bot"
kubectl -n "$NAMESPACE" get lease omnirelay-feishu-bot -w
```

将下面的字段合入 Go Deployment 的 Pod 模板。应用凭据和版本仍由部署注入。

```yaml
spec:
  serviceAccountName: omnirelay-feishu-bot
  containers:
    - name: omnirelay
      env:
        - name: OMNIRELAY_FEISHU_BOT_LEASE_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: OMNIRELAY_FEISHU_BOT_POD_UID
          valueFrom:
            fieldRef:
              fieldPath: metadata.uid
```

正常取消时，持有者先回收 SDK 子进程并使数据库连接代次失效，再释放 Lease。异常退出时由候选副本等待 Lease 到期后取得连接权。Lease 有效期为 30 秒，续租期限为 8 秒，重试间隔为 2 秒。

### 飞书长连接复验

适用官方 Go SDK `v3.13.0`（MIT）和 Kubernetes `client-go v0.35.0`（Apache-2.0）。更换 SDK 版本时复验握手、Pong、回执和退出。适配器通过该版本 SDK 的日志回调识别 Pong，原始日志内容不写入服务日志。

本地可使用 SDK 线协议测试记录回执状态、SDK 处理耗时和 WebSocket 往返耗时。数据库用例使用隔离 PostgreSQL，通过 `OMNIRELAY_TEST_DATABASE_URL` 注入连接地址。

```sh
go test ./internal/feishu -run 'Test(SDKWorker|Lease|ConnectionRestarts|TrustedMessageMapping)' -count=1 -v
go test ./tests/contract -run 'TestFeishu(Inbox|Connection)' -count=1 -v
```

使用授权的预发布应用与测试会话执行[三链路验收](../../design/engineering/feishu-integration-acceptance.md#预发布三链路执行与证据-20261117-2e4862a)。发布前由独立迁移 Job 执行 `omnirelay migrate`，并对两副本检查 `/readyz`。两个副本须使用同一预发布数据库、飞书应用身份和凭据代次。确认 HTTPS 入口证书有效，且调试跟踪、入口及 APM 已过滤 OAuth 回调查询串、请求凭据和响应令牌。

1. 启动两个 Go 副本，开启 Bot，观察 `holderIdentity`、连接代次和运行状态。等待首次握手后，在飞书开发者后台保存长连接方式并发布 `im.message.receive_v1`。
2. 验证同组织用户私聊和明确 @ 当前 Bot 的群消息。核对消息 ID、话题 ID、回复消息 ID 和 `last_message_at`。同时验证其他 Bot、无关 @ 及不匹配来源被过滤。
3. 依次验证停用、滚动交接、Lease 丢失、持有者进程重启和断线重连。逐次记录两副本角色、Lease 持有者、接管前后的数据库代次及心跳，核对替换代次开始时清空旧收信时间。单独暂停持有者续租并保持候选者可访问 Kubernetes API，待接管后恢复旧进程。在可控的晚到回调实验中记录旧持有者提交失败和零新增事件/工作。候选者接管本身只证明选主，旧代次拒绝提交还须核对存储结果。
4. 在隔离环境注入数据库提交失败和超过回调预算的等待，记录失败回执、飞书平台是否重投以及同一消息的工作记录数量。证据仅记录脱敏事件标识、版本、时间、响应状态和耗时。
5. 在维护窗口轮换预发布 App Secret。保持 App ID、租户、回调地址一致，退出旧副本并将新 Secret 与递增的 `OMNIRELAY_FEISHU_CREDENTIALS_GENERATION` 一并下发后，再恢复双副本和流量。旧代次副本不能用来回滚。需要恢复旧二进制时，先核实兼容性并继续使用当前有效 Secret 及代次。恢复服务后用新幂等键做一次检测，重新走 OAuth 换码、通讯录同步和 Bot 入站。
