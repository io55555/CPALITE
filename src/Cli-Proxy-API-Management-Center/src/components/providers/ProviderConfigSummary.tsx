import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { ModelAlias } from '@/types';
import { maskApiKey } from '@/utils/format';
import type { StatusBarData } from '@/utils/recentRequests';
import styles from '@/pages/AiProvidersPage.module.scss';
import { ProviderStatusBar } from './ProviderStatusBar';

interface ProviderSummaryField {
  label: ReactNode;
  value: ReactNode;
  tone?: 'default' | 'boolean';
}

interface ProviderConfigSummaryProps {
  title: ReactNode;
  apiKey: string;
  fields?: ProviderSummaryField[];
  headers?: [string, string][];
  models?: ModelAlias[];
  excludedModels?: string[];
  modelsLabel?: ReactNode;
  excludedModelsLabel?: ReactNode;
  disabledLabel?: ReactNode;
  stats: { success: number; failure: number };
  statusData: StatusBarData;
}

export function ProviderConfigSummary({
  title,
  apiKey,
  fields = [],
  headers = [],
  models,
  excludedModels,
  modelsLabel,
  excludedModelsLabel,
  disabledLabel,
  stats,
  statusData,
}: ProviderConfigSummaryProps) {
  const { t } = useTranslation();
  const visibleModels = models ?? [];
  const visibleExcludedModels = excludedModels ?? [];

  return (
    <div className={styles.providerCompactContent}>
      <div className={styles.providerCompactTopline}>
        <div className={styles.providerCompactIdentity}>
          <span className={styles.providerCompactTitle}>{title}</span>
          <span className={styles.providerCompactKey}>{maskApiKey(apiKey)}</span>
        </div>
        <div
          className={styles.providerCompactStats}
          aria-label={`${t('stats.success')} / ${t('stats.failure')}`}
        >
          <span className={`${styles.providerCompactStat} ${styles.statSuccess}`}>
            {t('stats.success')}: {stats.success}
          </span>
          <span className={`${styles.providerCompactStat} ${styles.statFailure}`}>
            {t('stats.failure')}: {stats.failure}
          </span>
        </div>
      </div>

      {fields.length || disabledLabel ? (
        <div className={styles.providerCompactFields}>
          {disabledLabel ? (
            <span className={`${styles.providerCompactChip} ${styles.providerCompactWarning}`}>
              {disabledLabel}
            </span>
          ) : null}
          {fields.map((field, index) => (
            <span
              key={`${String(field.label)}-${index}`}
              className={`${styles.providerCompactChip} ${
                field.tone === 'boolean' ? styles.providerCompactChipSoft : ''
              }`}
            >
              <span className={styles.providerCompactChipLabel}>{field.label}</span>
              <span className={styles.providerCompactChipValue}>{field.value}</span>
            </span>
          ))}
        </div>
      ) : null}

      {headers.length ? (
        <div className={styles.headerBadgeList}>
          {headers.map(([key, value]) => (
            <span key={key} className={styles.headerBadge}>
              <strong>{key}:</strong> {value}
            </span>
          ))}
        </div>
      ) : null}

      {visibleModels.length ? (
        <div className={`${styles.modelTagList} ${styles.providerCompactTagList}`}>
          <span className={styles.modelCountLabel}>
            {modelsLabel}: {visibleModels.length}
          </span>
          {visibleModels.map((model) => (
            <span key={model.name} className={styles.modelTag}>
              <span className={styles.modelName}>{model.name}</span>
              {model.alias && model.alias !== model.name && (
                <span className={styles.modelAlias}>{model.alias}</span>
              )}
            </span>
          ))}
        </div>
      ) : null}

      {visibleExcludedModels.length ? (
        <div className={styles.providerCompactExcluded}>
          <span className={styles.excludedModelsLabel}>{excludedModelsLabel}</span>
          <div className={`${styles.modelTagList} ${styles.providerCompactTagList}`}>
            {visibleExcludedModels.map((model) => (
              <span key={model} className={`${styles.modelTag} ${styles.excludedModelTag}`}>
                <span className={styles.modelName}>{model}</span>
              </span>
            ))}
          </div>
        </div>
      ) : null}

      <ProviderStatusBar statusData={statusData} />
    </div>
  );
}
