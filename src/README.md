# src 目录约定

| 路径 | 用途 |
| --- | --- |
| `src/CLIProxyAPI` | 本地开发的后端（可改） |
| `src/Cli-Proxy-API-Management-Center` | 本地开发的管理中心（可改） |
| `src/grok-manager` | grok-manager 源码（以 lib 形式编入 CPA） |
| `src原始代码/*` | **上游原版只读镜像**，禁止直接开发 |

## grok-manager
- 以 **内置模块** 方式编入 CPA，配置开关启用，**release 不发布 .so/.dll**
- 集成说明：`src/grok-manager/CPA_INTEGRATION.md`

## Linux 发布包
- `CPA_*_linux_<arch>.tar.gz`：glibc
- `CPA_*_linux_<arch>_musl.tar.gz`：Alpine musl
- `CPA_*_linux_<arch>_no-plugin.tar.gz`：静态便携（命名兼容；grok-manager 仍在二进制内）
- 升级脚本：`up.cpa.release.sh`
