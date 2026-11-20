# Kubernetes 部署目录

连接、迁移、密钥、备份和网络拓扑见 [OmniRelay Kubernetes 部署设计](../../design/specs/omni-relay-k8s-deployment-design.md)。

## 目标主机工作目录

在运行 Kubernetes 客户端和构建工具的目标主机，用 `workspace/OmniRelay` 保存工程。构建镜像、生成环境配置、校验清单、执行部署和验证都在目标主机运行。

| 目录 | 用途 |
| --- | --- |
| `base/namespace/` | `omni` 命名空间 |
| `base/minio/` | HTTPS S3 服务、PVC 与访问策略 |
| `scripts/` | 部署、探针和读写检查 |
| `overlays/` | 可公开且不含环境实际值的示例 |
| `private/` | 目标主机本地目录，保存真实入口参数、镜像仓库地址、证书、凭据与覆盖文件。此目录从 Git 忽略 |

真实入口域名、SSH 隧道参数、节点地址及端口、Feishu 和数据库凭据均在目标主机设置。工作负载需要的凭据通过 Kubernetes Secret 注入。外部 Nginx 和 SSH 只转发工作台及 OAuth 路由到 Go Web 服务。MinIO 仅提供集群内部 ClusterIP。

## 部署顺序

1. 核对 Kubernetes context 和本地制品。需要 Docker 的环境先在目标主机拉取或构建可用镜像。在私有配置里可用 `MINIO_IMAGE` 指向对应的镜像摘要或已构建的目标主机镜像。
2. 创建 `omni` 命名空间，创建 MinIO 数据卷，再部署及验证文件存储。
3. 配置 PostgreSQL TLS、运行和迁移身份，执行迁移 Job。
4. 按版本发布 Go、DSH 桥和运行时，通过各自探针后，再配置目标主机的 Web NodePort。确认外部 TLS、OAuth 回调和内网依赖。
5. 通过应用身份访问 MinIO 时，给 Go Pod 加 `omnirelay.io/s3-access: "true"`，挂载 CA，并配置仅能操作应用 bucket 的 S3 身份。

### 镜像仓库直连不可用时

从下文指定版本的上游制品取得与目标节点架构匹配的静态 `silo`，按上游校验清单核对后，将其放入目标主机的 `deploy/k8s/private/assets/silo/`。也可从固定摘要的上游镜像提取对应平台二进制并校验传输前后的 SHA256。在目标主机为其构建镜像，再将镜像引用写入私有环境配置。多节点部署应把构建结果推送到内部镜像仓库。

```bash
docker build --network=none \
  -f deploy/k8s/base/minio/image.Dockerfile \
  -t omnirelay/silo:<release> deploy/k8s/private/assets/silo
printf 'MINIO_IMAGE=%s\n' 'omnirelay/silo:<release>' > deploy/k8s/private/minio-image.env
```

### MinIO 兼容文件存储

清单使用 [PGSTY Silo 的固定版本](https://github.com/pgsty/silo/releases/tag/RELEASE.2026-11-12T00-00-00Z)。该版本兼容 MinIO 的环境变量、管理接口和 S3 API。清单按单节点部署，使用 Rancher local-path 和专用 Retain 存储类。集群采用其他存储后端时调整 StorageClass 并复用已有 PVC；首次部署前确定存储配置。

```bash
context="$(kubectl config current-context)"
deploy/k8s/scripts/apply-object-storage.sh "$context"
deploy/k8s/scripts/verify-object-storage.sh "$context"
deploy/k8s/scripts/configure-object-storage-access.sh "$context"
```

部署脚本在目标主机 `private/minio/` 下生成凭据及内部 CA，复用已有凭据。凭据及 TLS 私钥通过 K8s Secret 提供，CA 公钥通过 ConfigMap 提供。S3 域名为 `minio.omni.svc`。服务以 HTTPS 监听 9000，管理控制台关闭。目录中的 CA 和服务证书需要按有效期轮换。备份对象和 CA 时继续使用受控的私有存储。

验证脚本通过目标主机回环地址临时转发，校验证书链和服务名，创建或复用私有 bucket `omnirelay-files`，写入和读回临时对象，再验证匿名访问被拒绝并删除测试对象。首次部署可传 `--restart` 验证 Pod 重建后仍能读回同一对象。已有业务流量时安排重建窗口。

应用授权脚本使用目标主机上的 `mcli`，可用 `MINIO_ADMIN_CLIENT` 指定可执行文件。它创建或复用 `omnirelay-files` 和该 bucket 专用的读写身份，验证写入、读回和禁止管理操作，并生成 `Secret/omnirelay-s3` 给 Go。管理凭据只用于初始化。应用权限包含对象读写和分段上传，普通应用请求没有删除权限。

Go 清单与脚本作为部署模板。使用待配置页面及 MinIO 就绪检查时，镜像须包含 `OMNIRELAY_SETUP_MODE` 和 `OMNIRELAY_S3_*` 的实现。构建上下文按 `.dockerignore` 清单提供 Go 源码、前端源码、原型共用资源、依赖锁和构建测试夹具。`private/` 留在目标主机。启用飞书 Bot 时，按下方说明合入 Lease 服务账号和 Pod 环境变量。

## 机器人消息发送

`scripts/apply-go.sh` 默认向 Go Pod 注入 `OMNIRELAY_FEISHU_BOT_SEND_ENABLED=true`。可在目标主机的 `private/route.env` 中设为 `false`，再运行发布脚本关闭发送。直接运行 Go 服务时须显式配置此开关。发送需要已配置并启用的飞书机器人、应用的消息发送权限和目标会话权限。群聊中的 @ 候选来自当前群成员，发送时会再次核验成员资格。

## 飞书 OAuth 入口来源

Go 默认按 TCP 对端地址限制飞书登录发起。目标主机通过 Nginx、SSH 与私有 NodePort 转发时，多个用户可能共用同一个 TCP 对端。生产和预发布应在私有入口配置中启用以下信任方式。

1. 在目标主机生成独立的 32 字节随机令牌并编码为十六进制。通过私有配置把相同值提供给 Nginx 和 `Secret/omnirelay-oauth-proxy` 的 `token` 键。Go 清单会从该键读取 `OMNIRELAY_OAUTH_PROXY_TOKEN`。创建或轮换 Secret 后重建 Go Pod，使环境变量生效。
2. 在私有覆盖中为 Go 配置 `OMNIRELAY_TLS_CERT_FILE` 和 `OMNIRELAY_TLS_KEY_FILE`。Nginx 经 SSH 隧道使用 HTTPS 连接 Go，并校验上游证书链和服务名。保留 Web NodePort 的私有访问限制。
3. 在实际代理到 Go 的 Nginx `location` 内覆盖两项头部，并保留该路由原有的代理头设置。客户端 IP 来自 Nginx 观察到的 `$remote_addr`。如前面另有负载均衡，只对已核实的上游地址启用 Nginx 的真实 IP 恢复。令牌由私有 include 注入，入口日志、APM 和响应均须排除令牌及 OAuth 查询串。

```nginx
proxy_set_header X-Omnirelay-Client-IP $remote_addr;
proxy_set_header X-Omnirelay-Proxy-Token "<通过私有 include 注入>";
```

在目标环境确认普通请求提供的同名头会被覆盖，并用不同测试来源验证发起额度隔离。Go 仅在 TLS 直接连接上接受携带正确令牌的单个 IP 头。令牌缺失、错误或头部格式异常时，回退到 TCP 对端地址。Go 不读取 `Forwarded`、`X-Forwarded-For` 或 `X-Real-IP` 来分配额度。

每实例每分钟最多接受 120 次发起，每个来源最多 20 次。新发起的回跳携带与浏览器绑定的短期签名，已验证数据库事务使用独立的每分钟 240 次额度。升级或凭据轮换前发起的旧事务仍可在有限的未验证回跳额度内完成。各副本应使用相同飞书凭据并保持时钟同步。

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

### 工作台升级与恢复

1. 在目标主机从干净的发布提交执行 `docker build -f deploy/k8s/base/go/image.Dockerfile -t "$IMAGE" .`。按既有流程将镜像放入目标仓库，记录完整的 `仓库@sha256:摘要`。镜像编译双入口并内嵌到 Go 中，运行镜像的 `/opt/omnirelay/ui/assets` 供同源入口提取。
2. 首次从旧 DOM 版本切换时，先对仍提供旧工作台的入口运行 `python3 deploy/k8s/scripts/publish-ui.py legacy "$OMNI_PUBLIC_ORIGIN"`。它只复制上一版公开的脚本、样式和图片。将目标主机的 `/var/lib/omnirelay/ui` 以只读方式提供给每个入口 Nginx，在原有 HTTPS server 引用 `base/go/nginx/ui.conf`，检查配置并平滑重载。域名、证书、API、OAuth、HTML 和初始化请求沿用原有转发。首次安装可直接启用同源配置。
3. 在可写入该归档且可访问镜像仓库的部署主机运行 `deploy/k8s/scripts/apply-go.sh "$CONTEXT" "$IMAGE_DIGEST"`。脚本先发布上一版与本版哈希资源，逐文件校验同源响应和归档标记，随后执行迁移、记录上一镜像摘要并滚动升级。多个入口须共享同一归档。保留两个版本及仍可能由打开页面引用的更早资源；归档独立于 Pod 和发布工作目录。
4. 使用候选版本的测试账号核对深链、OAuth 回跳、权限变化和首次配置。在旧页面保持打开时加载任务详情，再从新页面触发延迟加载，对照两次网络响应的内容和 MIME。暂时拦截任务详情脚本，确认出现刷新提示，解除拦截并刷新后可恢复。
5. 在确认当前数据库结构与上一镜像兼容后，运行 `deploy/k8s/scripts/rollback-go.sh "$CONTEXT"`。脚本用 Deployment 记录的上一镜像摘要恢复应用容器并等待就绪，沿用当前凭据代次。核对 `/readyz`、登录和已打开新页面的延迟加载。数据库和静态归档无需回退。
