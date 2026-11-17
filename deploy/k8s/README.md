# Kubernetes 部署目录

资源拆分、连接、迁移、密钥与备份设计见 [OmniRelay Kubernetes 部署设计](../../design/specs/omni-relay-k8s-deployment-design.md)。

- `base/` 放 Go Deployment、Web 和工具 Service、DSH StatefulSet 与 Service、选主和网络资源，以及独立迁移 Job。
- `overlays/staging/` 放测试飞书应用所用的入口、配置及密钥引用。
- `overlays/production/` 放生产入口、资源和存储配置，以及生产密钥引用。

目录当前用于部署文件归位。清单随阶段零验证补齐。
