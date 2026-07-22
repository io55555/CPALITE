# Upstream Baselines

## CLIProxyAPI
- branch: main
- commit: f71ec0eb6776854457892452cf28c47f0d658251
- tag: v7.2.95
- note: selectively merged and validated against local enhancement-preserving overrides; retained packet capture/filtering, status-rulers, provider enhancements, quota display fixes, OpenAI-compatible cooldown/candidate-skip behavior, auth-file cooldown views, UA/request logging, proxy-failure 3-minute cooldown behavior, and added provider-specific Codex/xAI quota cooldown configuration plus xAI packet-filter cooldown handling

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
