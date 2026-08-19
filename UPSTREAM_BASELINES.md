# Upstream Baselines

## CLIProxyAPI
- branch: main
- commit: 497673bf6bdab783b767641bb5228e914960a879
- tag: v7.2.136
- note: selective merge from v7.2.131..v7.2.136: adopted compatible translator/schema/registry, GPT-5.6 context metadata, Claude fingerprint helpers, custom-header $ resolution, session-cache Touch/CompareAndDelete/stopOnce, DisableCoolingOverride explicit-false metadata, Result.CredentialScope/Options, exported request-scoped error codes, and focused tests. Kept local conductor/selector/scheduler/auth-files/server/config/executor/packet-filter/Grok Manager monoliths to preserve cooldown, OpenWebUI, quota, monitoring, SSOcookie, and packet-capture chains. Official conductor/config/executor file splits, selector OnResult/canonical affinity keys, credential DisableCooling *bool, and RequestScopedErrorRule config wiring were not adopted. Official Antigravity provenance/replay snapshot rewrite remains not adopted. Rollback: rollback/pre-v7.2.136-v1.22.6-merge-20260819-175314.

## Cli-Proxy-API-Management-Center
- branch: main
- commit: 6586f88858ca27e840bd8db2630dccd371a1cd4a
- tag: v1.22.6
- note: selective merge from v1.22.2..v1.22.6: adopted compatible config-page field/i18n/sponsor updates (Bestproxy) while keeping local packet/filter/auth cooldown/Grok Manager/custom config/monitoring surfaces; xAI Grok Build/OpenWebUI, Codex identity, sidebar-brand hide, and auth-files first/last pagination remain local.

## CLIProxyAPI-Pro
- repository: https://github.com/ssfun/CLIProxyAPI-Pro
- branch: main
- commit: d013a136ea8d8541ac0ba480752a43527bed499f
- tag: v7.1.19-pro
- note: 2026-05-24 checked latest v7.1.19-pro / 6c42247177fee1661687e785a272a3c133852036, analysis only, not merged into current base
