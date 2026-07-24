# Upstream Baselines

## CLIProxyAPI
- branch: main
- commit: 42f36b94e0805a9897c3aa3be46a2b124be0057e
- tag: v7.2.97
- note: selectively merged new files + unmodified upstream paths from v7.2.95..v7.2.97; kept heavily customized local chains (service/handlers/server/websockets/config/packet/cooldown) and carefully upgraded auth conductor for home concurrency compatibility; Management Center remains v1.18.6 (already latest)

## Cli-Proxy-API-Management-Center
- branch: main
- commit: 3738c0b7ff21ce7e1423795a26769fff05fd81d6
- tag: v1.18.6
- note: selectively merged and validated with local Provider Workbench/quota/auth-files preservation; retained local Ampcode/provider-class enhancements, auth-files cooldown view behavior, packet-capture rule UI, and added upstream xAI paid-quota health fallback wiring

## CLIProxyAPI-Pro
- repository: https://github.com/ssfun/CLIProxyAPI-Pro
- branch: main
- commit: d013a136ea8d8541ac0ba480752a43527bed499f
- tag: v7.1.19-pro
- note: 2026-05-24 checked latest v7.1.19-pro / 6c42247177fee1661687e785a272a3c133852036, analysis only, not merged into current base


关键记录：
======================================================================
• 结论：429 现在有两条冷却路径，都会写同一套 auth 冷却状态；如果同一次请求两条都命中，后执行的抓包规则会覆盖普通 429 配置冷却。

  同时存在时

  以 xAI 429 + 规则 [运营商到CPA]xai响应码429冷却24h 为例：

  1. xAI executor 收到上游 429。
  2. executor 先执行抓包规则匹配。
  3. 如果规则命中 cooldown：
      - 发布 packet filter action：cooldown/api_key/86400s
      - 同时把本次错误的 RetryAfter 设为 86400s

  4. auth manager MarkResult 处理失败：
      - 普通 429 分支先按 RetryAfter 或 quota 配置计算冷却。
      - 然后 applyPacketFilterActionState 再按抓包规则 cooldown-seconds 写一次冷却。

  5. 最终结果：账号和模型进入冷却，NextRetryAfter = now + 抓包规则秒数。你的规则是 86400，所以最终是 24 小时。

  如果抓包规则秒数和配置不同，最终以抓包规则为准，因为它在普通 429 分支之后覆盖。

  只有配置项时

  没有抓包规则命中时，就走普通 429 quota 冷却：

  - Codex 账号：
      - 使用 codex-quota-cooldown-base-seconds
      - 使用 codex-quota-cooldown-max-seconds
      - 你的配置是第一次 429 冷却 86400 秒，后续按 backoff 翻倍，最高 604800 秒。

  - xAI 账号：
      - 使用 xai-quota-cooldown-base-seconds
      - 使用 xai-quota-cooldown-max-seconds
      - 你的配置是 base/max 都 86400，所以每次 429 都是 24 小时，不会增长到更长。

  还有一个优先级：如果 executor 从上游错误里解析到了明确的 RetryAfter，普通 429 分支会优先用 RetryAfter，而不是 base/max 配置。xAI 的 free-usage-exhausted 会主动给 24h RetryAfter；
  普通 xAI 429 没有明确 retry hint 时才用 xai-quota-cooldown-*。

  需要特别说明：codex-quota-cooldown-* 在当前代码里实际是“非 xAI 的 quota 冷却默认边界”，Codex 会用它；xAI 不用它，xAI 走 xai-quota-cooldown-*。
  ======================================================================
  