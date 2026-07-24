# grok-manager 本地维护说明（CPA 集成）

> 开发副本：`src/grok-manager`（可改）  
> 上游原版：`src原始代码/grok-manager`（只读，合并上游时对照用）

## 代理策略（v1.3.7+）
测活 / 隔离复测 / SSO 转换 / 401 重刷出站请求优先级：
1. 认证文件 `proxy_url`（或 metadata.proxy_url；`direct` 表示强制直连）
2. CPA 配置文件顶层 `proxy-url`
3. 直连

## 发布
由 monorepo `.github/workflows/release_test.yml` 交叉编译并注入：
- `plugins/linux/amd64/grok-manager.so`
- `plugins/linux/arm64/grok-manager.so`
- `plugins/windows/amd64/grok-manager.dll`

## 安全注意
- SSO vault（`sso-vault.json`）含完整 SSO Cookie，权限 0600；备份/导出接口会带出明文，务必保护 management 密钥
- 管理 API 依赖 CPA management 鉴权，不要对公网裸奔 management 端口

## Release 资产命名（GitHub）
GitHub Release 资产是扁平文件名，必须带 os/arch：
- `grok-manager-linux-amd64.so`
- `grok-manager-linux-arm64.so`
- `grok-manager-windows-amd64.dll`
- 版本化：`grok-manager-v1.3.7-linux-amd64.so` 等

CPA 压缩包内路径（运行时加载）：
- `plugins/linux/amd64/grok-manager.so`
- `plugins/linux/arm64/grok-manager.so`
- `plugins/windows/amd64/grok-manager.dll`

服务器升级脚本 `up.cpa.release.sh` 默认会同步安装对应架构插件。