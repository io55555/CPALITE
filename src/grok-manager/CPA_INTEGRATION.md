# grok-manager 本地维护说明（CPA 集成）

> 开发副本：`src/grok-manager`（可改）  
> 上游原版：`src原始代码/grok-manager`（只读）

## 使用方式（全平台统一）

grok-manager **已编译进 CPA 主程序**（in-process builtin），**不再发布/依赖** `.so` / `.dll`。

`config.yaml` 启用：

```yaml
plugins:
  enabled: true
  configs:
    grok-manager:
      enabled: true
```

停用：将 `enabled: false`，或关闭 `plugins.enabled`。

日志成功标志：`pluginhost: plugin loaded`，路径为 `builtin://grok-manager`。

可选环境变量：
- `CPA_GROK_MANAGER_BUILTIN=0`：强制走动态库模式（需自备 `.so`，不推荐）
- 默认 builtin=开启

## 代理策略（v1.3.7+）
1. 认证文件 `proxy_url`（或 metadata.proxy_url；`direct` 强制直连）
2. CPA 配置顶层 `proxy-url`
3. 直连

## 代码结构
- `src/grok-manager/lib`：纯 Go 业务库（被 CPA `replace` 引入）
- `src/grok-manager/plugin`：可选 c-shared 入口（仅开发调试动态库，**release 不打包**）
- CPA 接入：`src/CLIProxyAPI/internal/pluginhost/builtin_grok_manager.go`

## 发布
 monorepo `release_test.yml` 只发布 CPA 压缩包 + `management.html`，**不发布插件文件**。

## 安全注意
- SSO vault 含明文 Cookie，权限 0600；保护 management 密钥
- 不要对公网裸露 management 端口
