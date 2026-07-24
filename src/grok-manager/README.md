# grok-manager

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）原生插件，面向 **xAI / Grok** 账号池运维。

当前版本：**v1.1.2**（完整版，含 SSO → CPA；含 Windows / Linux 预编译；硬隔离）

## 功能

| 模块 | 说明 |
| --- | --- |
| 测活 | 并发探测 `xai` 凭证，汇总健康 / 401 / 402 / 403 / 429 |
| 清理 | 按候选 / HTTP 状态 / 文件名删除 |
| 运行时隔离 | `usage.handle` 写入隔离表；`scheduler.pick` **硬跳过**坏号直至解封 |
| 429 策略 | 固定 **2 小时**硬顶；到期复测，仍限流再 +2h |
| 邮箱主键 | 隔离按 email 去重；usage 无邮箱时从 auth 反填 |
| 定时 | 周期扫描 / 复检 / 可选 401 自动从 vault 重刷 |
| **SSO 转换** | SSO Cookie → CPA xAI OAuth 凭证 |
| **SSO 历史库** | vault 持久化、预览、导出、401 重刷 |
| 面板 | CPA 管理 UI 内嵌 **Grok Manager** |

### 默认隔离时长

| 上游状态 | 时长 |
| --- | --- |
| 401 | 24h（vault 有 SSO 时更短，方便自动重刷） |
| 402 | 7 天 |
| 403 | 24 小时 |
| 429 | 2 小时（到期复测） |

只处理 `xai` provider。

## 预编译二进制

从 [Releases](https://github.com/1296018244/grok-manager/releases) 下载对应系统文件：

| 系统 | Release 资产 | 放到 CPA 目录 |
| --- | --- | --- |
| Windows amd64 | `grok-manager.dll` 或 `grok-manager-windows-amd64.dll` | `plugins/windows/amd64/grok-manager.dll` |
| Linux amd64 | `grok-manager.so` 或 `grok-manager-linux-amd64.so` | `plugins/linux/amd64/grok-manager.so` |
| Linux arm64 | `grok-manager-linux-arm64.so` | `plugins/linux/arm64/grok-manager.so` |

> 动态库在 Linux 上是 `.so`，在 Windows 上是 `.dll`。架构必须与 CPA 进程一致（amd64 主机不要用 arm64 包）。  
> **ARM 预编译**：Release 提供 `linux/arm64`；也可在 arm64 机器上自行 `CGO_ENABLED=1 go build -buildmode=c-shared`。

## 安装（让插件「生效 / 注册」）

插件商店只写配置、或只下载了错误平台的文件时，面板会出现：

- **已配置**：`config.yaml` 里有 `plugins.configs.grok-manager`
- **未注册 / 未生效**：当前系统的动态库没加载成功

### 1. 放对二进制

```text
# Windows amd64
plugins/windows/amd64/grok-manager.dll

# Linux amd64
plugins/linux/amd64/grok-manager.so

# Linux arm64（树莓派 64 位 / 部分 ARM 云主机）
plugins/linux/arm64/grok-manager.so
# 或保留架构名：plugins/linux/arm64/grok-manager-linux-arm64.so
```

也可用版本名：`grok-manager-v1.1.2.dll` / `grok-manager-v1.1.2.so` / `grok-manager-linux-arm64.so`（CPA 会识别 `plugin_id=grok-manager`）。

### 2. 配置启用

```yaml
plugins:
  enabled: true          # 全局必须开
  dir: plugins
  configs:
    grok-manager:
      enabled: true      # 本插件必须开
```

### 3. 重启 CPA

重启后日志应出现：

```text
pluginhost: plugin loaded plugin_id=grok-manager ...
pluginhost: plugin registered plugin_id=grok-manager ... version=1.1.2
```

### 4. 打开面板

管理面板侧栏进入 **Grok Manager**（需有效管理密钥）。

### 状态对照

| 面板标签 | 含义 | 处理 |
| --- | --- | --- |
| 已配置 + 未注册 | 有配置，无可用动态库 | 按系统放入 `.dll` / `.so` 后重启 |
| 已注册 + 未生效 | 已加载但未参与调度/能力 | 确认 `enabled: true` 与全局 `plugins.enabled` |
| 已生效 | 正常 | 侧栏应有 Grok Manager |

## 管理 API

基路径：`/v0/management/plugins/grok-manager`

主要接口：测活 `/scan`、结果 `/results`、隔离 `/bans`、定时 `/schedule`、  
SSO `/sso-import` `/sso-vault` `/sso-refresh-401`、备份 `/backup` 等。详见面板与源码路由注册。

## 构建

需要 **Go 1.22+**、**CGO**。

### Windows

```bat
build-windows.bat
```

### Linux（本机）

```bash
chmod +x build.sh && ./build.sh
```

### Linux（推荐 Docker，与官方 Debian 镜像兼容）

```bash
docker run --rm \
  -v "$PWD:/src" \
  -w /src \
  golang:1.24-bookworm \
  sh -c 'CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o dist/grok-manager-linux-amd64.so .'
```

## 数据目录

```text
plugins/grok-manager/last-scan.json
plugins/grok-manager/schedule.json
plugins/grok-manager/bans.json
plugins/grok-manager/sso-vault.json
plugins/grok-manager/last-sso-import.json
```

## 友链

学 AI 上 L 站！[L 站链接](https://linux.do)

## 致谢

- 运行时隔离思路参考 [akihitohyh/xai-autoban](https://github.com/akihitohyh/xai-autoban)（MIT）
- CLIProxyAPI：[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)

## License

[MIT](LICENSE)
