# grok-manager v1.0.2

## 变更

- **429 隔离硬顶 2h**：`clamp429ResetAt`，历史过长 `ResetAt` 会被裁剪
- **邮箱主键隔离**：按 email 去重；usage 无邮箱时从 auth 反填
- **冷启动 `/bans` 不再超时**：UI 路径只用邮箱缓存，全量 `auth.list` 后台 heal
- 菜单显示为 **Grok Manager (Public)**，与私有 CPA 版区分
- 仍**不含** SSO Cookie 转 CPA / SSO 历史库

## 安装（Windows）

将资源放到 CPA 插件目录：

```text
plugins/windows/amd64/grok-manager.dll
# 或
plugins/windows/amd64/grok-manager-v1.0.2.dll
```

配置：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    grok-manager:
      enabled: true
```

重启后日志应出现 `version=1.0.2`。

## 不包含

- SSO Cookie → CPA 凭证
- SSO vault / 401 自动从库重刷
