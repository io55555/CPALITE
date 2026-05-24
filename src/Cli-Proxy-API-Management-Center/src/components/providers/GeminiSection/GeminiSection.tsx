import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import iconGemini from '@/assets/icons/gemini.svg';
import type { GeminiKeyConfig } from '@/types';
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

interface GeminiSectionProps {
  configs: GeminiKeyConfig[];
  usageByProvider: ProviderRecentUsageMap;
  loading: boolean;
  disableControls: boolean;
  isSwitching: boolean;
  onAdd: () => void;
  onEdit: (index: number) => void;
  onDelete: (index: number) => void;
  onToggle: (index: number, enabled: boolean) => void;
}

export function GeminiSection({
  configs,
  usageByProvider,
  loading,
  disableControls,
  isSwitching,
  onAdd,
  onEdit,
  onDelete,
  onToggle,
}: GeminiSectionProps) {
  const { t } = useTranslation();
  const { mode, setMode } = useProviderDisplayMode('gemini');
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
          getProviderRecentBuckets(usageByProvider, 'gemini', config.apiKey, config.baseUrl)
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
            <img src={iconGemini} alt="" className={styles.cardTitleIcon} />
            {t('ai_providers.gemini_title')}
            <ProviderDisplayModeControl value={mode} onChange={setMode} />
          </span>
        }
        extra={
          <Button size="sm" onClick={onAdd} disabled={actionsDisabled}>
            {t('ai_providers.gemini_add_button')}
          </Button>
        }
      >
        <ProviderList<GeminiKeyConfig>
          items={configs}
          loading={loading}
          keyField={(item, index) => getProviderConfigKey(item, index)}
          emptyTitle={t('ai_providers.gemini_empty_title')}
          emptyDescription={t('ai_providers.gemini_empty_desc')}
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
          getRowDisabled={(item) => item.disabled === true || hasDisableAllModelsRule(item.excludedModels)}
          renderExtraActions={(item, index) => (
            <ToggleSwitch
              label={t('ai_providers.config_toggle_label')}
              checked={item.disabled !== true && !hasDisableAllModelsRule(item.excludedModels)}
              disabled={toggleDisabled}
              onChange={(value) => void onToggle(index, value)}
            />
          )}
          renderContent={(item, index) => {
            const stats = getProviderTotalStats(
              usageByProvider,
              'gemini',
              item.apiKey,
              item.baseUrl
            );
            const headerEntries = Object.entries(item.headers || {});
            const configDisabled = item.disabled === true || hasDisableAllModelsRule(item.excludedModels);
            const excludedModels = item.excludedModels ?? [];
            const statusData =
              statusBarCache.get(getProviderConfigKey(item, index)) ||
              statusBarDataFromRecentRequests([]);

            return (
              <ProviderConfigSummary
                title={`${t('ai_providers.gemini_item_title')} #${index + 1}`}
                apiKey={item.apiKey}
                mode={mode}
                stats={stats}
                statusData={statusData}
                headers={headerEntries}
                models={item.models}
                modelsLabel={t('ai_providers.gemini_models_count')}
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
                ]}
              />
            );
          }}
        />
      </Card>
    </>
  );
}
