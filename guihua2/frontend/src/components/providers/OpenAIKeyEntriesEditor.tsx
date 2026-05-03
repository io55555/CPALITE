import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { authFilesApi } from '@/services/api';
import type { ApiKeyEntry } from '@/types';
import type { KeyTestStatus } from '@/stores/useOpenAIEditDraftStore';
import { buildApiKeyEntry } from '@/components/providers/utils';
import styles from '@/pages/AiProvidersPage.module.scss';

type Props = {
  entries: ApiKeyEntry[];
  saving: boolean;
  disableControls: boolean;
  isTestingKeys: boolean;
  hasConfiguredModels: boolean;
  keyTestStatuses: Array<{ status: KeyTestStatus['status']; message: string } | undefined>;
  onEntriesChange: (entries: ApiKeyEntry[]) => void;
  onResetStatuses: (length: number) => void;
  onResetTestSummary: () => void;
  onTestSingleKey: (index: number) => Promise<boolean>;
  showNotification: (message: string, type?: 'success' | 'error' | 'warning' | 'info') => void;
};

function StatusLoadingIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" className={styles.statusIconSpin}>
      <circle cx="8" cy="8" r="7" stroke="currentColor" strokeOpacity="0.25" strokeWidth="2" />
      <path d="M8 1A7 7 0 0 1 8 15" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

function StatusSuccessIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="8" fill="var(--success-color, #22c55e)" />
      <path d="M4.5 8L7 10.5L11.5 6" stroke="white" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function StatusErrorIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="8" fill="var(--danger-color, #c65746)" />
      <path d="M5 5L11 11M11 5L5 11" stroke="white" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function StatusIdleIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="7" stroke="var(--text-tertiary, #9ca3af)" strokeWidth="2" />
    </svg>
  );
}

function StatusIcon({ status }: { status: KeyTestStatus['status'] }) {
  switch (status) {
    case 'loading':
      return <StatusLoadingIcon />;
    case 'success':
      return <StatusSuccessIcon />;
    case 'error':
      return <StatusErrorIcon />;
    default:
      return <StatusIdleIcon />;
  }
}

function getRuntimeState(entry: ApiKeyEntry) {
  if (entry.disabled || entry.status === 'disabled') return { label: '已停用', tone: 'error' as const };
  if (entry.status === 'error' || entry.lastError) return { label: '异常', tone: 'warning' as const };
  if (entry.statusMessage) return { label: '冻结中', tone: 'warning' as const };
  return { label: '启用', tone: 'success' as const };
}

function getRuntimeChip(entry: ApiKeyEntry): string {
  const statusMessage = String(entry.statusMessage ?? '').trim();
  const lastError = String(entry.lastError ?? '').trim();
  const errorLower = lastError.toLowerCase();
  const statusLower = statusMessage.toLowerCase();
  if (entry.disabled || entry.status === 'disabled') return '手动停用';
  if (statusLower.includes('status-ruler')) return '规则停用';
  if (errorLower.includes('proxy') || errorLower.includes('connect') || errorLower.includes('timeout')) return '网络故障';
  if (errorLower.includes('unauthorized') || errorLower.includes('organization_restricted')) return '账号故障';
  if (statusMessage || lastError) return '自动停用';
  return '';
}

export function OpenAIKeyEntriesEditor(props: Props) {
  const { t } = useTranslation();
  const {
    entries,
    saving,
    disableControls,
    isTestingKeys,
    hasConfiguredModels,
    keyTestStatuses,
    onEntriesChange,
    onResetStatuses,
    onResetTestSummary,
    onTestSingleKey,
    showNotification,
  } = props;
  const [detail, setDetail] = useState<{ title: string; body: string } | null>(null);
  const list = entries.length ? entries : [buildApiKeyEntry()];

  const updateEntry = (idx: number, field: keyof ApiKeyEntry, value: string) => {
    const next = list.map((entry, i) => (i === idx ? { ...entry, [field]: value } : entry));
    onEntriesChange(next);
    onResetStatuses(next.length);
    onResetTestSummary();
  };

  const removeEntry = (idx: number) => {
    const next = list.filter((_, i) => i !== idx);
    const normalized = next.length ? next : [buildApiKeyEntry()];
    onEntriesChange(normalized);
    onResetStatuses(normalized.length);
    onResetTestSummary();
  };

  const addEntry = () => {
    const next = [...list, buildApiKeyEntry()];
    onEntriesChange(next);
    onResetStatuses(next.length);
    onResetTestSummary();
  };

  const patchRuntime = async (idx: number, action: 'enable' | 'disable') => {
    const target = list[idx];
    const authIndex = String(target.authIndex ?? '').trim();
    if (!authIndex) {
      showNotification('当前 API key 缺少 auth-index，无法切换启用状态', 'error');
      return;
    }
    try {
      await authFilesApi.patchRuntimeState(authIndex, action);
      const next = list.map((entry, i) =>
        i !== idx
          ? entry
          : {
              ...entry,
              disabled: action === 'disable',
              status: action === 'disable' ? 'disabled' : 'active',
              statusMessage: action === 'disable' ? 'disabled via management' : '',
              lastError: '',
            }
      );
      onEntriesChange(next);
      showNotification(action === 'disable' ? '已停用该 API key' : '已启用该 API key', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '切换 API key 状态失败', 'error');
    }
  };

  return (
    <div className={styles.keyEntriesList}>
      <div className={styles.keyEntriesToolbar}>
        <span className={styles.keyEntriesCount}>
          {t('ai_providers.openai_keys_count')}: {list.length}
        </span>
        <Button
          variant="secondary"
          size="sm"
          onClick={addEntry}
          disabled={saving || disableControls || isTestingKeys}
          className={styles.addKeyButton}
        >
          {t('ai_providers.openai_keys_add_btn')}
        </Button>
      </div>
      <div className={styles.keyTableShell}>
        <div className={styles.keyTableHeader}>
          <div className={styles.keyTableColIndex}>#</div>
          <div className={styles.keyTableColStatus}>{t('common.status')}</div>
          <div className={styles.keyTableColKey}>{t('common.api_key')}</div>
          <div className={styles.keyTableColProxy}>{t('common.proxy_url')}</div>
          <div className={styles.keyTableColRuntime}>运行状态</div>
          <div className={styles.keyTableColAction}>{t('common.action')}</div>
        </div>
        {list.map((entry, index) => {
          const keyStatus = keyTestStatuses[index]?.status ?? 'idle';
          const canTestKey = Boolean(entry.apiKey?.trim()) && hasConfiguredModels;
          const runtimeState = getRuntimeState(entry);
          const runtimeChip = getRuntimeChip(entry);
          return (
            <div key={index} className={styles.keyTableRow}>
              <div className={styles.keyTableColIndex}>{index + 1}</div>
              <div className={styles.keyTableColStatus} title={keyTestStatuses[index]?.message || ''}>
                <StatusIcon status={keyStatus} />
              </div>
              <div className={styles.keyTableColKey}>
                <input
                  type="text"
                  value={entry.apiKey}
                  onChange={(e) => updateEntry(index, 'apiKey', e.target.value)}
                  disabled={saving || disableControls || isTestingKeys}
                  className={`input ${styles.keyTableInput}`}
                  placeholder={t('ai_providers.openai_key_placeholder')}
                />
              </div>
              <div className={styles.keyTableColProxy}>
                <input
                  type="text"
                  value={entry.proxyUrl ?? ''}
                  onChange={(e) => updateEntry(index, 'proxyUrl', e.target.value)}
                  disabled={saving || disableControls || isTestingKeys}
                  className={`input ${styles.keyTableInput}`}
                  placeholder={t('ai_providers.openai_proxy_placeholder')}
                />
              </div>
              <div className={styles.keyTableColRuntime}>
                <div className={styles.keyRuntimeBlock}>
                  <span className={`status-badge ${runtimeState.tone}`}>{runtimeState.label}</span>
                  {runtimeChip ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        setDetail({
                          title: `API key #${index + 1} 运行时详情`,
                          body: [
                            `Auth Index: ${String(entry.authIndex ?? '-')}`,
                            `状态说明: ${String(entry.statusMessage ?? '-')}`,
                            `错误详情: ${String(entry.lastError ?? '-')}`,
                            '',
                            '说明：更完整的上游请求/响应头、响应内容、status-ruler 命中规则与时间，仍需后端继续补结构化返回。',
                          ].join('\n'),
                        })
                      }
                    >
                      {runtimeChip}
                    </Button>
                  ) : null}
                </div>
              </div>
              <div className={styles.keyTableColAction}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() =>
                    void patchRuntime(index, entry.disabled || entry.status === 'disabled' ? 'enable' : 'disable')
                  }
                  disabled={saving || disableControls || isTestingKeys || !String(entry.authIndex ?? '').trim()}
                >
                  {entry.disabled || entry.status === 'disabled' ? '启用' : '停用'}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void onTestSingleKey(index)}
                  disabled={saving || disableControls || isTestingKeys || !canTestKey}
                  loading={keyStatus === 'loading'}
                >
                  {t('ai_providers.openai_test_single_action')}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => removeEntry(index)}
                  disabled={saving || disableControls || isTestingKeys || list.length <= 1}
                >
                  {t('common.delete')}
                </Button>
              </div>
            </div>
          );
        })}
      </div>
      <Modal open={detail !== null} onClose={() => setDetail(null)} title={detail?.title ?? '运行时详情'} width={760}>
        <div className={styles.codeBlock}>{detail?.body ?? ''}</div>
      </Modal>
    </div>
  );
}
