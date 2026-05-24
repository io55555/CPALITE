import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import iconAmp from '@/assets/icons/amp.svg';
import type { AmpcodeConfig } from '@/types';
import { maskApiKey } from '@/utils/format';
import styles from '@/pages/AiProvidersPage.module.scss';
import { useTranslation } from 'react-i18next';

interface AmpcodeSectionProps {
  config: AmpcodeConfig | null | undefined;
  loading: boolean;
  disableControls: boolean;
  isSwitching: boolean;
  onEdit: () => void;
}

export function AmpcodeSection({
  config,
  loading,
  disableControls,
  isSwitching,
  onEdit,
}: AmpcodeSectionProps) {
  const { t } = useTranslation();
  const showLoadingPlaceholder = loading && !config;

  return (
    <>
      <Card
        title={
          <span className={styles.cardTitle}>
            <img src={iconAmp} alt="" className={styles.cardTitleIcon} />
            {t('ai_providers.ampcode_title')}
          </span>
        }
        extra={
          <Button
            size="sm"
            onClick={onEdit}
            disabled={disableControls || loading || isSwitching}
          >
            {t('common.edit')}
          </Button>
        }
      >
        {showLoadingPlaceholder ? (
          <div className="hint">{t('common.loading')}</div>
        ) : (
          <div className={styles.ampcodeCompactContent}>
            <div className={styles.providerCompactFields}>
              <span className={styles.providerCompactChip}>
                <span className={styles.providerCompactChipLabel}>
                  {t('ai_providers.ampcode_upstream_url_label')}
                </span>
                <span className={styles.providerCompactChipValue}>
                  {config?.upstreamUrl || t('common.not_set')}
                </span>
              </span>
              <span className={styles.providerCompactChip}>
                <span className={styles.providerCompactChipLabel}>
                  {t('ai_providers.ampcode_upstream_api_key_label')}
                </span>
                <span className={styles.providerCompactChipValue}>
                  {config?.upstreamApiKey ? maskApiKey(config.upstreamApiKey) : t('common.not_set')}
                </span>
              </span>
              <span className={`${styles.providerCompactChip} ${styles.providerCompactChipSoft}`}>
                <span className={styles.providerCompactChipLabel}>
                  {t('ai_providers.ampcode_force_model_mappings_label')}
                </span>
                <span className={styles.providerCompactChipValue}>
                  {(config?.forceModelMappings ?? false) ? t('common.yes') : t('common.no')}
                </span>
              </span>
              <span className={styles.providerCompactChip}>
                <span className={styles.providerCompactChipLabel}>
                  {t('ai_providers.ampcode_model_mappings_count')}
                </span>
                <span className={styles.providerCompactChipValue}>
                  {config?.modelMappings?.length || 0}
                </span>
              </span>
              <span className={styles.providerCompactChip}>
                <span className={styles.providerCompactChipLabel}>
                  {t('ai_providers.ampcode_upstream_api_keys_count')}
                </span>
                <span className={styles.providerCompactChipValue}>
                  {config?.upstreamApiKeys?.length || 0}
                </span>
              </span>
            </div>
            {config?.upstreamApiKeys?.length ? (
              <div className={`${styles.modelTagList} ${styles.providerCompactTagList}`}>
                {config.upstreamApiKeys.map((entry, index) => (
                  <span key={`${entry.upstreamApiKey}-${index}`} className={styles.modelTag}>
                    <span className={styles.modelName}>{maskApiKey(entry.upstreamApiKey)}</span>
                    <span className={styles.modelAlias}>{entry.apiKeys?.length || 0}</span>
                  </span>
                ))}
              </div>
            ) : null}
            {config?.modelMappings?.length ? (
              <div className={`${styles.modelTagList} ${styles.providerCompactTagList}`}>
                {config.modelMappings.map((mapping) => (
                  <span key={`${mapping.from}→${mapping.to}`} className={styles.modelTag}>
                    <span className={styles.modelName}>{mapping.from}</span>
                    <span className={styles.modelAlias}>{mapping.to}</span>
                  </span>
                ))}
              </div>
            ) : null}
          </div>
        )}
      </Card>
    </>
  );
}
