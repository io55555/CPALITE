import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import iconClaude from '@/assets/icons/claude.svg';
import type { ProviderKeyConfig } from '@/types';
import { statusBarDataFromRecentRequests } from '@/utils/recentRequests';
import styles from '@/pages/AiProvidersPage.module.scss';
import { ProviderList } from '../ProviderList';
import { ProviderConfigSummary } from '../ProviderConfigSummary';
import { ProviderDisplayModeControl, useProviderDisplayMode } from '../ProviderDisplayMode';
import {
  getProviderConfigKey,
  getProviderRecentBuckets,
  getProviderTotalStats,
  hasDisableAllModelsRule,
  type ProviderRecentUsageMap,
} from '../utils';

interface ClaudeSectionProps {
  configs: ProviderKeyConfig[];
  usageByProvider: ProviderRecentUsageMap;
  loading: boolean;
  disableControls: boolean;
  isSwitching: boolean;
  onAdd: () => void;
  onEdit: (index: number) => void;
  onDelete: (index: number) => void;
  onToggle: (index: number, enabled: boolean) => void;
}

export function ClaudeSection({
  configs,
  usageByProvider,
  loading,
  disableControls,
  isSwitching,
  onAdd,
  onEdit,
  onDelete,
  onToggle,
}: ClaudeSectionProps) {
  const { t } = useTranslation();
  const { mode, setMode } = useProviderDisplayMode('claude');
  const actionsDisabled = disableControls || loading || isSwitching;
  const toggleDisabled = disableControls || loading || isSwitching;

  const statusBarCache = useMemo(() => {
    const cache = new Map<string, ReturnType<typeof statusBarDataFromRecentRequests>>();

    configs.forEach((config, index) => {
      if (!config.apiKey) return;
      const configKey = getProviderConfigKey(config, index);
      cache.set(
        configKey,
        statusBarDataFromRecentRequests(
          getProviderRecentBuckets(usageByProvider, 'claude', config.apiKey, config.baseUrl)
        )
      );
    });

    return cache;
  }, [configs, usageByProvider]);

  return (
    <>
      <Card
        title={
          <span className={styles.cardTitle}>
            <img src={iconClaude} alt="" className={styles.cardTitleIcon} />
            {t('ai_providers.claude_title')}
            <ProviderDisplayModeControl value={mode} onChange={setMode} />
          </span>
        }
        extra={
          <Button size="sm" onClick={onAdd} disabled={actionsDisabled}>
            {t('ai_providers.claude_add_button')}
          </Button>
        }
      >
        <ProviderList<ProviderKeyConfig>
          items={configs}
          loading={loading}
          keyField={(item, index) => getProviderConfigKey(item, index)}
          emptyTitle={t('ai_providers.claude_empty_title')}
          emptyDescription={t('ai_providers.claude_empty_desc')}
          onEdit={(_, index) => onEdit(index)}
          onDelete={(_, index) => onDelete(index)}
          actionsDisabled={actionsDisabled}
          listClassName={
            mode === 'original'
              ? undefined
              : mode === 'badge'
                ? styles.badgeProviderList
                : styles.compactProviderList
          }
          rowClassName={
            mode === 'original'
              ? undefined
              : mode === 'badge'
                ? styles.badgeProviderRow
                : styles.compactProviderRow
          }
          metaClassName={mode === 'original' ? undefined : styles.compactProviderMeta}
          actionsClassName={mode === 'original' ? undefined : styles.compactProviderActions}
          getRowDisabled={(item) => hasDisableAllModelsRule(item.excludedModels)}
          renderExtraActions={(item, index) => (
            <ToggleSwitch
              label={t('ai_providers.config_toggle_label')}
              checked={!hasDisableAllModelsRule(item.excludedModels)}
              disabled={toggleDisabled}
              onChange={(value) => void onToggle(index, value)}
            />
          )}
          renderContent={(item, index) => {
            const stats = getProviderTotalStats(
              usageByProvider,
              'claude',
              item.apiKey,
              item.baseUrl
            );
            const headerEntries = Object.entries(item.headers || {});
            const configDisabled = hasDisableAllModelsRule(item.excludedModels);
            const excludedModels = item.excludedModels ?? [];
            const statusData =
              statusBarCache.get(getProviderConfigKey(item, index)) ||
              statusBarDataFromRecentRequests([]);

            const cloakMode = (() => {
              const raw = (item.cloak?.mode ?? '').trim().toLowerCase();
              const key = raw === 'always' || raw === 'never' ? raw : 'auto';
              return t(`ai_providers.claude_cloak_mode_${key}`);
            })();

            return (
              <ProviderConfigSummary
                title={t('ai_providers.claude_item_title')}
                apiKey={item.apiKey}
                mode={mode}
                stats={stats}
                statusData={statusData}
                headers={headerEntries}
                models={item.models}
                modelsLabel={t('ai_providers.claude_models_count')}
                excludedModels={excludedModels}
                excludedModelsLabel={t('ai_providers.excluded_models_count', {
                  count: excludedModels.length,
                })}
                disabledLabel={configDisabled ? t('ai_providers.config_disabled_badge') : undefined}
                fields={[
                  ...(item.priority !== undefined
                    ? [{ label: t('common.priority'), value: item.priority }]
                    : []),
                  ...(item.prefix ? [{ label: t('common.prefix'), value: item.prefix }] : []),
                  ...(item.baseUrl ? [{ label: t('common.base_url'), value: item.baseUrl }] : []),
                  ...(item.proxyUrl ? [{ label: t('common.proxy_url'), value: item.proxyUrl }] : []),
                  ...(item.cloak
                    ? [{ label: t('ai_providers.claude_cloak_mode_label'), value: cloakMode }]
                    : []),
                  ...(item.cloak?.strictMode
                    ? [
                        {
                          label: t('ai_providers.claude_cloak_strict_label'),
                          value: t('common.yes'),
                          tone: 'boolean' as const,
                        },
                      ]
                    : []),
                  ...(item.cloak?.sensitiveWords?.length
                    ? [
                        {
                          label: t('ai_providers.claude_cloak_sensitive_words_count'),
                          value: item.cloak.sensitiveWords.length,
                        },
                      ]
                    : []),
                ]}
              />
            );
          }}
        />
      </Card>
    </>
  );
}
