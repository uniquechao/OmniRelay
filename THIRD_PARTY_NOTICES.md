# 第三方许可证与来源

OmniRelay 自有内容使用 [MIT License](LICENSE)。以下浏览器分发文件由原型直接使用，随原型副本一起分发，并保留各自的版权与许可条款。

| 项目 | 版本及分发包 | 原型文件 | 许可证 |
| --- | --- | --- | --- |
| [Quill](https://github.com/slab/quill) | [quill 2.0.3](https://www.npmjs.com/package/quill/v/2.0.3) | `quill-2.0.3.js`、`quill-snow-2.0.3.css` | [BSD-3-Clause](design/prototype/vendor/quill-LICENSE.txt) · [上游包版权声明](design/prototype/vendor/quill-bundle-LICENSE.txt) |
| [markdown-it](https://github.com/markdown-it/markdown-it) | [markdown-it 15.0.3](https://www.npmjs.com/package/markdown-it/v/15.0.3) | `markdown-it-15.0.3.min.js` | [MIT](design/prototype/vendor/markdown-it-LICENSE.txt) |
| [highlight.js](https://github.com/highlightjs/highlight.js) | [@highlightjs/cdn-assets 11.11.1](https://www.npmjs.com/package/@highlightjs/cdn-assets/v/11.11.1) | `highlight-11.11.1.min.js` | [BSD-3-Clause](design/prototype/vendor/highlight-LICENSE.txt) |

Quill 和 markdown-it 浏览器包内嵌模块的许可文本见 [内嵌依赖许可证](design/prototype/vendor/BUNDLED-DEPENDENCIES-LICENSES.txt)。模块范围依据上游分发包的 source map，版本依据相应发布标签的锁文件。 [Quill 锁文件](https://github.com/slab/quill/blob/v2.0.3/package-lock.json) · [markdown-it 锁文件](https://github.com/markdown-it/markdown-it/blob/15.0.3/package-lock.json)

README 中的第三方图形和品牌链接用于标明来源，相关权利归各权利人。项目 Bot 页按需加载 DeepSeek Harness 0.2.2-rc.2 的会话容器、编辑器、输入栏、消息组件与主题；运行时使用同版本 SDK。DeepSeek SuperAgent 研究原型是设计参考。DSH 组件版权归 DeepSeek，按 [MIT License](https://github.com/deepseek-ai/deepseek-harness/blob/dsh-v0.2.2-rc.2/LICENSE) 分发。构建会将依赖许可文本写入 `web/dist/assets/third-party-licenses-<hash>.txt`。项目引用见 [开源项目与致谢](README.md#开源项目与致谢)。

开发工具位于 `.agents/skills/`，相应许可证如下。

| 项目 | 位置 | 许可证 |
| --- | --- | --- |
| [Impeccable](https://github.com/pbakaus/impeccable) | `.agents/skills/impeccable/` | [Apache-2.0](.agents/skills/impeccable/LICENSE) |
| [modern-screenshot](https://github.com/qq15725/modern-screenshot) | Impeccable 附带的 `scripts/modern-screenshot.umd.js` | [MIT](.agents/skills/impeccable/scripts/modern-screenshot-LICENSE.txt) |
| [Vercel Web Design Guidelines](https://github.com/vercel-labs/agent-skills/tree/main/skills/web-design-guidelines) | `.agents/skills/web-design-guidelines/` | [MIT](.agents/skills/web-design-guidelines/LICENSE)，[上游声明](https://github.com/vercel-labs/agent-skills#license) |

聊天文档卡片使用飞书开放平台提供的文档、电子表格、多维表格、妙记、知识库和文件图标，资源保存在 `web/feishu-icons/`。图标权利归各自权利人。[飞书图标枚举](https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/enumerations-for-icons)

项目任务看板移植自 OmniGrid 的《任务看板独立复刻交付包》（2026-11-22）。看板、任务检查器、候选审核、设置面板及其样式位于 `frontend/src/features/task-board/`。菜单和弹层使用 Radix UI，组合框和任务检查器使用 React Aria Components，图标使用 Lucide React，拼音搜索使用 pinyin-pro。对应版本记录在 `package-lock.json`。
