# grok-manager 本地维护说明（CPA 集成）

> 开发副本：`src/grok-manager`（可改）  
> 上游原版：`src原始代码/grok-manager`（只读，合并上游时对照用）

## 代理策略（v1.3.7+）
测活 / 隔离复测 / SSO 转换 / 401 重刷出站请求优先级：
1. 认证文件 `proxy_url`（或 metadata.proxy_url；`direct` 表示强制直连）
2. CPA 配置文件顶层 `proxy-url`
3. 直连

## 发布与插件加载

由 monorepo `.github/workflows/release_test.yml` 交叉编译并注入。

### Linux 包矩阵

| 资产 | 适用系统 | 能否加载插件 |
|---|---|---|
| `CPA_<ver>_linux_<arch>.tar.gz` | Ubuntu/Debian (glibc) | 能（CGO=1） |
| `CPA_<ver>_linux_<arch>_musl.tar.gz` | Alpine (musl) | 能（CGO=1 musl） |
| `CPA_<ver>_linux_<arch>_no-plugin.tar.gz` | 任意（静态） | 不能 |

**主机与插件必须同一 libc**：glibc 主机配 glibc `.so`，Alpine 配 `*-musl.so`。

### 插件资产命名（GitHub 扁平文件）

- glibc: `grok-manager-linux-amd64.so` / `grok-manager-linux-arm64.so`
- musl: `grok-manager-linux-amd64-musl.so` / `grok-manager-linux-arm64-musl.so`
- windows: `grok-manager-windows-amd64.dll`
- 版本化：`grok-manager-v1.3.7-linux-arm64-musl.so` 等

### 压缩包内运行时路径

- `plugins/linux/amd64/grok-manager.so`
- `plugins/linux/arm64/grok-manager.so`
- `plugins/windows/amd64/grok-manager.dll`

### 升级脚本

`up.cpa.release.sh` 默认：
- 检测 Alpine/musl -> 优先 `*_musl`，否则 `*_no-plugin`
- 检测 glibc -> 优先默认插件包，否则 `*_no-plugin`
- 可用 `--package musl|glibc|no-plugin` 强制选型
- 可用 `--skip-plugin` 跳过插件

Alpine 用户若仍看到：
`standard dynamic library plugin loading requires cgo on this platform`
说明装到了 `*_no-plugin` 静态包或旧的 CGO=0 包，请改用 `*_musl` 包后重启。

## 安全注意
- SSO vault（`sso-vault.json`）含完整 SSO Cookie，权限 0600；备份/导出接口会带出明文，务必保护 management 密钥
- 管理 API 依赖 CPA management 鉴权，不要对公网裸露 management 端口
