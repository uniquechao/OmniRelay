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

issue 10 使用授权的预发布应用与测试会话完成平台验收。

1. 启动两个 Go 副本，开启 Bot，观察 `holderIdentity`、连接代次和运行状态。等待首次握手后，在飞书开发者后台保存长连接方式并发布 `im.message.receive_v1`。
2. 验证同组织用户私聊和明确 @ 当前 Bot 的群消息。核对消息 ID、话题 ID、回复消息 ID 和 `last_message_at`。同时验证其他 Bot、无关 @ 及不匹配来源被过滤。
3. 依次验证停用、滚动交接、凭据换代和断线重连。核对旧代次停止入站，替换代次开始时清空旧收信时间。中断连接后核对状态变化和心跳失效，恢复后核对新的握手与心跳。
4. 在隔离环境注入数据库提交失败和超过回调预算的等待，记录失败回执、飞书平台是否重投以及同一消息的工作记录数量。证据仅记录脱敏事件标识、版本、时间、响应状态和耗时。
