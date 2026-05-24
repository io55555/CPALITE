import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { ModelAlias } from '@/types';
import { maskApiKeyWithVisibleChars } from '@/utils/format';
import type { StatusBarData } from '@/utils/recentRequests';
import styles from '@/pages/AiProvidersPage.module.scss';
import type { ProviderDisplayMode } from './ProviderDisplayMode';
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
  mode: ProviderDisplayMode;
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
  mode,
}: ProviderConfigSummaryProps) {
  const { t } = useTranslation();
  const visibleModels = models ?? [];
  const visibleExcludedModels = excludedModels ?? [];
  const maskedKey = maskApiKeyWithVisibleChars(apiKey, 3);

  const statusWithStats = (
    <div className={styles.providerCompactStatusLine}>
      <ProviderStatusBar statusData={statusData} />
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
  );

  if (mode === 'original') {
    return (
      <>
        <div className="item-title">{title}</div>
        <div className={styles.fieldRow}>
          <span className={styles.fieldLabel}>{t('common.api_key')}:</span>
          <span className={styles.fieldValue}>{maskedKey}</span>
        </div>
        {fields.map((field, index) => (
          <div key={`${String(field.label)}-${index}`} className={styles.fieldRow}>
            <span className={styles.fieldLabel}>{field.label}:</span>
            <span className={styles.fieldValue}>{field.value}</span>
          </div>
        ))}
        {headers.length ? (
          <div className={styles.headerBadgeList}>
            {headers.map(([key, value]) => (
              <span key={key} className={styles.headerBadge}>
                <strong>{key}:</strong> {value}
              </span>
            ))}
          </div>
        ) : null}
        {disabledLabel ? (
          <div className="status-badge warning" style={{ marginTop: 8, marginBottom: 0 }}>
            {disabledLabel}
          </div>
        ) : null}
        {visibleModels.length ? (
          <div className={styles.modelTagList}>
            <span className={styles.modelCountLabel}>
              {modelsLabel}: {visibleModels.length}
            </span>
            {visibleModels.map((model) => (
              <span key={`${model.name}-${model.alias || 'default'}`} className={styles.modelTag}>
                <span className={styles.modelName}>{model.name}</span>
                {model.alias && model.alias !== model.name && (
                  <span className={styles.modelAlias}>{model.alias}</span>
                )}
              </span>
            ))}
          </div>
        ) : null}
        {visibleExcludedModels.length ? (
          <div className={styles.excludedModelsSection}>
            <div className={styles.excludedModelsLabel}>{excludedModelsLabel}</div>
            <div className={styles.modelTagList}>
              {visibleExcludedModels.map((model) => (
                <span key={model} className={`${styles.modelTag} ${styles.excludedModelTag}`}>
                  <span className={styles.modelName}>{model}</span>
                </span>
              ))}
            </div>
          </div>
        ) : null}
        {statusWithStats}
      </>
    );
  }

  return (
    <div
      className={`${styles.providerCompactContent} ${
        mode === 'badge' ? styles.providerBadgeContent : ''
      }`}
    >
      <div className={styles.providerCompactTopline}>
        <div className={styles.providerCompactIdentity}>
          <span className={styles.providerCompactTitle}>{title}</span>
          <span className={styles.providerCompactKey}>{maskedKey}</span>
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

      {statusWithStats}
    </div>
  );
}
