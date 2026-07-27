# Upstream Baselines

## CLIProxyAPI
- branch: main
- commit: 8423cce2d1004e80948a9e2c60ee69354c0aabc3
- tag: v7.2.102
- note: selective merge from v7.2.97..v7.2.102: applied safe updates + pure new modules (codex models/multi-agent helpers, postgres cooldown store, session identity, signature carrier); skipped upstream structural file-splits of heavily customized monoliths (server/config/handlers/conductor/xai/codex/claude/antigravity/service) to preserve local enhancement chains (packet/filter/cooldown/OpenWebUI/Grok Manager/quota/auth-files); Codex Live media relay not adopted (depends on full config/service split).

## Cli-Proxy-API-Management-Center
- branch: main
- commit: 21af57620b45f5e159e5450bc7e702498b664639
- tag: v1.19.3
- note: selective merge from v1.18.6..v1.19.3: safe updates + new apiError/authFilesEvents/tests; three-way merged non-protected pages; kept local AuthFiles/quota/visual-config/packet-related surfaces on conflict to preserve cooldown UI and provider-class enhancements; upstream OAuth manual-refresh UI partially not ported into local AuthFilesPage.

## CLIProxyAPI-Pro
- repository: https://github.com/ssfun/CLIProxyAPI-Pro
- branch: main
- commit: d013a136ea8d8541ac0ba480752a43527bed499f
- tag: v7.1.19-pro
- note: 2026-05-24 checked latest v7.1.19-pro / 6c42247177fee1661687e785a272a3c133852036, analysis only, not merged into current base
