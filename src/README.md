# src 目录约定

| 路径 | 用途 |
| --- | --- |
| `src/CLIProxyAPI` | 本地开发的后端（可改） |
| `src/Cli-Proxy-API-Management-Center` | 本地开发的管理中心（可改） |
| `src/grok-manager` | 本地开发的 grok-manager 插件（可改） |
| `src原始代码/*` | **上游原版只读镜像**，禁止直接开发；合并上游时从此对照/同步 |

## grok-manager
- 开发与发布构建以 `src/grok-manager` 为准
- 上游原版：`src原始代码/grok-manager`（保持与官方仓库一致，便于 diff/合并）
- 集成说明：`src/grok-manager/CPA_INTEGRATION.md`

## Linux 发布包（插件）
- `CPA_*_linux_<arch>.tar.gz`：glibc（Ubuntu/Debian），可加载插件
- `CPA_*_linux_<arch>_musl.tar.gz`：musl（Alpine），可加载插件
- `CPA_*_linux_<arch>_no-plugin.tar.gz`：静态包，不可加载插件
- 升级脚本 `up.cpa.release.sh` 会按 libc 自动选择；详见 `src/grok-manager/CPA_INTEGRATION.md`
