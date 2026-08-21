import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconGithub, IconBookOpen, IconExternalLink, IconCode } from '@/components/ui/icons';
import {
  useAuthStore,
  useConfigStore,
  useNotificationStore,
  useModelsStore,
  useThemeStore,
} from '@/stores';
import { configApi, runtimeMetricsApi, type RuntimeMetrics, versionApi } from '@/services/api';
import { useApiKeysForModels } from '@/hooks/useApiKeysForModels';
import { formatDateTimeValue } from '@/utils/format';
import { classifyModels } from '@/utils/models';
import { STORAGE_KEY_AUTH } from '@/utils/constants';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import iconGemini from '@/assets/icons/gemini.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconOpenaiLight from '@/assets/icons/openai-light.svg';
import iconOpenaiDark from '@/assets/icons/openai-dark.svg';
import iconQwen from '@/assets/icons/qwen.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconGlm from '@/assets/icons/glm.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconGrokDark from '@/assets/icons/grok-dark.svg';
import iconDeepseek from '@/assets/icons/deepseek.svg';
import iconMinimax from '@/assets/icons/minimax.svg';
import styles from './SystemPage.module.scss';

const MODEL_CATEGORY_ICONS: Record<string, string | { light: string; dark: string }> = {
  gpt: { light: iconOpenaiLight, dark: iconOpenaiDark },
  claude: iconClaude,
  gemini: iconGemini,
  qwen: iconQwen,
  kimi: { light: iconKimiDark, dark: iconKimiLight },
  glm: iconGlm,
  grok: { light: iconGrok, dark: iconGrokDark },
  deepseek: iconDeepseek,
  minimax: iconMinimax,
};

const parseVersionSegments = (version?: string | null) => {
  if (!version) return null;
  const cleaned = version.trim().replace(/^v/i, '');
  if (!cleaned) return null;
  const parts = cleaned
    .split(/[^0-9]+/)
    .filter(Boolean)
    .map((segment) => Number.parseInt(segment, 10))
    .filter(Number.isFinite);
  return parts.length ? parts : null;
};

const compareVersions = (latest?: string | null, current?: string | null) => {
  const latestParts = parseVersionSegments(latest);
  const currentParts = parseVersionSegments(current);
  if (!latestParts || !currentParts) return null;
  const length = Math.max(latestParts.length, currentParts.length);
  for (let i = 0; i < length; i++) {
    const l = latestParts[i] || 0;
    const c = currentParts[i] || 0;
    if (l > c) return 1;
    if (l < c) return -1;
  }
  return 0;
};

const RUNTIME_REFRESH_OPTIONS = [
  { value: '60000', label: '1分钟' },
  { value: '300000', label: '5分钟' },
  { value: '900000', label: '15分钟' },
  { value: '0', label: '手动' },
];

const formatBytes = (value: unknown) => {
  const bytes = typeof value === 'number' && Number.isFinite(value) ? value : 0;
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let next = bytes;
  let index = 0;
  while (next >= 1024 && index < units.length - 1) {
    next /= 1024;
    index += 1;
  }
  return `${next >= 10 ? next.toFixed(1) : next.toFixed(2)} ${units[index]}`;
};

const formatPercent = (value: unknown) => {
  const num = typeof value === 'number' && Number.isFinite(value) ? value : 0;
  return `${num.toFixed(2)}%`;
};

const formatNumber = (value: unknown) => {
  const num = typeof value === 'number' && Number.isFinite(value) ? value : 0;
  return new Intl.NumberFormat().format(num);
};

const formatSeconds = (value: unknown) => {
  const seconds = typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0;
  const days = Math.floor(seconds / 86_400);
  const hours = Math.floor((seconds % 86_400) / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);
  if (days > 0) return `${days}天 ${hours}小时`;
  if (hours > 0) return `${hours}小时 ${minutes}分钟`;
  return `${minutes}分钟`;
};

const formatNanoseconds = (value: unknown) => {
  const ns = typeof value === 'number' && Number.isFinite(value) ? value : 0;
  if (ns <= 0) return '0 ms';
  return `${(ns / 1_000_000).toFixed(2)} ms`;
};

const formatBoolean = (value: unknown) => (value === true ? '是' : '否');

const formatUnixSeconds = (value: unknown, language: string) => {
  const seconds = typeof value === 'number' && Number.isFinite(value) ? value : 0;
  if (seconds <= 0) return '-';
  return formatDateTimeValue(new Date(seconds * 1000).toISOString(), language);
};

export function SystemPage() {
  const { t, i18n } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const auth = useAuthStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const clearCache = useConfigStore((state) => state.clearCache);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);

  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);

  const [modelStatus, setModelStatus] = useState<{
    type: 'success' | 'warning' | 'error' | 'muted';
    message: string;
  }>();
  const [requestLogModalOpen, setRequestLogModalOpen] = useState(false);
  const [requestLogDraft, setRequestLogDraft] = useState(false);
  const [requestLogTouched, setRequestLogTouched] = useState(false);
  const [requestLogSaving, setRequestLogSaving] = useState(false);
  const [checkingVersion, setCheckingVersion] = useState(false);
  const [runtimeMetrics, setRuntimeMetrics] = useState<RuntimeMetrics | null>(null);
  const [runtimeLoading, setRuntimeLoading] = useState(false);
  const [runtimeError, setRuntimeError] = useState('');
  const [runtimeRefreshMs, setRuntimeRefreshMs] = useState(300_000);

  const versionTapCount = useRef(0);
  const versionTapTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const otherLabel = useMemo(
    () => (i18n.language?.toLowerCase().startsWith('zh') ? '其他' : 'Other'),
    [i18n.language]
  );
  const groupedModels = useMemo(() => classifyModels(models, { otherLabel }), [models, otherLabel]);
  const requestLogEnabled = config?.requestLog ?? false;
  const requestLogDirty = requestLogDraft !== requestLogEnabled;
  const canEditRequestLog = auth.connectionStatus === 'connected' && Boolean(config);

  const appVersion = __APP_VERSION__ || t('system_info.version_unknown');
  const apiVersion = auth.serverVersion || t('system_info.version_unknown');
  const buildTime =
    formatDateTimeValue(auth.serverBuildDate, i18n.language) || t('system_info.version_unknown');

  const getIconForCategory = (categoryId: string): string | null => {
    const iconEntry = MODEL_CATEGORY_ICONS[categoryId];
    if (!iconEntry) return null;
    if (typeof iconEntry === 'string') return iconEntry;
    return resolvedTheme === 'dark' ? iconEntry.dark : iconEntry.light;
  };

  const resolveApiKeysForModels = useApiKeysForModels();

  const fetchModels = async ({ forceRefresh = false }: { forceRefresh?: boolean } = {}) => {
    if (auth.connectionStatus !== 'connected') {
      setModelStatus({
        type: 'warning',
        message: t('notification.connection_required'),
      });
      return;
    }

    if (!auth.apiBase) {
      showNotification(t('notification.connection_required'), 'warning');
      return;
    }

    setModelStatus({ type: 'muted', message: t('system_info.models_loading') });
    try {
      const apiKeys = await resolveApiKeysForModels({ force: forceRefresh });
      const primaryKey = apiKeys[0];
      const list = await fetchModelsFromStore(auth.apiBase, primaryKey, forceRefresh);
      const hasModels = list.length > 0;
      setModelStatus({
        type: hasModels ? 'success' : 'warning',
        message: hasModels
          ? t('system_info.models_count', { count: list.length })
          : t('system_info.models_empty'),
      });
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : typeof err === 'string' ? err : '';
      const suffix = message ? `: ${message}` : '';
      const text = `${t('system_info.models_error')}${suffix}`;
      setModelStatus({ type: 'error', message: text });
    }
  };

  const handleClearLoginStorage = () => {
    showConfirmation({
      title: t('system_info.clear_login_title', { defaultValue: 'Clear Login Storage' }),
      message: t('system_info.clear_login_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: () => {
        auth.logout();
        if (typeof localStorage === 'undefined') return;
        const keysToRemove = [STORAGE_KEY_AUTH, 'isLoggedIn', 'apiBase', 'apiUrl', 'managementKey'];
        keysToRemove.forEach((key) => localStorage.removeItem(key));
        showNotification(t('notification.login_storage_cleared'), 'success');
      },
    });
  };

  const openRequestLogModal = useCallback(() => {
    setRequestLogTouched(false);
    setRequestLogDraft(requestLogEnabled);
    setRequestLogModalOpen(true);
  }, [requestLogEnabled]);

  const handleInfoVersionTap = useCallback(() => {
    versionTapCount.current += 1;
    if (versionTapTimer.current) {
      clearTimeout(versionTapTimer.current);
    }

    if (versionTapCount.current >= 7) {
      versionTapCount.current = 0;
      versionTapTimer.current = null;
      openRequestLogModal();
      return;
    }

    versionTapTimer.current = setTimeout(() => {
      versionTapCount.current = 0;
      versionTapTimer.current = null;
    }, 1500);
  }, [openRequestLogModal]);

  const handleRequestLogClose = useCallback(() => {
    setRequestLogModalOpen(false);
    setRequestLogTouched(false);
  }, []);

  const handleRequestLogSave = async () => {
    if (!canEditRequestLog) return;
    if (!requestLogDirty) {
      setRequestLogModalOpen(false);
      return;
    }

    const previous = requestLogEnabled;
    setRequestLogSaving(true);
    updateConfigValue('request-log', requestLogDraft);

    try {
      await configApi.updateRequestLog(requestLogDraft);
      clearCache('request-log');
      showNotification(t('notification.request_log_updated'), 'success');
      setRequestLogModalOpen(false);
    } catch (error: unknown) {
      const message =
        error instanceof Error ? error.message : typeof error === 'string' ? error : '';
      updateConfigValue('request-log', previous);
      showNotification(
        `${t('notification.update_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    } finally {
      setRequestLogSaving(false);
    }
  };

  const handleVersionCheck = useCallback(async () => {
    setCheckingVersion(true);
    try {
      const data = await versionApi.checkLatest();
      const latestRaw = data?.['latest-version'] ?? data?.latest_version ?? data?.latest ?? '';
      const latest = typeof latestRaw === 'string' ? latestRaw : String(latestRaw ?? '');
      const comparison = compareVersions(latest, auth.serverVersion);

      if (!latest) {
        showNotification(t('system_info.version_check_error'), 'error');
        return;
      }

      if (comparison === null) {
        showNotification(t('system_info.version_current_missing'), 'warning');
        return;
      }

      if (comparison > 0) {
        showNotification(t('system_info.version_update_available', { version: latest }), 'warning');
      } else {
        showNotification(t('system_info.version_is_latest'), 'success');
      }
    } catch (error: unknown) {
      const message =
        error instanceof Error ? error.message : typeof error === 'string' ? error : '';
      const suffix = message ? `: ${message}` : '';
      showNotification(`${t('system_info.version_check_error')}${suffix}`, 'error');
    } finally {
      setCheckingVersion(false);
    }
  }, [auth.serverVersion, showNotification, t]);

  const fetchRuntimeMetrics = useCallback(async () => {
    if (auth.connectionStatus !== 'connected') return;
    setRuntimeLoading(true);
    setRuntimeError('');
    try {
      setRuntimeMetrics(await runtimeMetricsApi.get());
    } catch (error: unknown) {
      const message =
        error instanceof Error ? error.message : typeof error === 'string' ? error : '';
      setRuntimeError(message || '运行数据加载失败');
    } finally {
      setRuntimeLoading(false);
    }
  }, [auth.connectionStatus]);

  useEffect(() => {
    fetchConfig().catch(() => {
      // ignore
    });
  }, [fetchConfig]);

  useEffect(() => {
    if (requestLogModalOpen && !requestLogTouched) {
      setRequestLogDraft(requestLogEnabled);
    }
  }, [requestLogModalOpen, requestLogTouched, requestLogEnabled]);

  useEffect(() => {
    return () => {
      if (versionTapTimer.current) {
        clearTimeout(versionTapTimer.current);
      }
    };
  }, []);

  useEffect(() => {
    fetchModels({ forceRefresh: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth.connectionStatus, auth.apiBase]);

  useEffect(() => {
    void fetchRuntimeMetrics();
  }, [fetchRuntimeMetrics]);

  useEffect(() => {
    if (runtimeRefreshMs <= 0 || auth.connectionStatus !== 'connected') return undefined;
    const timer = window.setInterval(() => {
      void fetchRuntimeMetrics();
    }, runtimeRefreshMs);
    return () => window.clearInterval(timer);
  }, [auth.connectionStatus, fetchRuntimeMetrics, runtimeRefreshMs]);

  return (
    <div className={styles.container}>
      <h1 className={styles.pageTitle}>{t('system_info.title')}</h1>
      <div className={styles.content}>
        <Card className={styles.aboutCard}>
          <div className={styles.aboutHeader}>
            <img src={INLINE_LOGO_JPEG} alt="CPAMC" className={styles.aboutLogo} />
            <div className={styles.aboutTitle}>{t('system_info.about_title')}</div>
          </div>

          <div className={styles.aboutInfoGrid}>
            <button
              type="button"
              className={`${styles.infoTile} ${styles.tapTile}`}
              onClick={handleInfoVersionTap}
            >
              <div className={styles.tileHeader}>
                <div className={styles.tileLabel}>{t('footer.version')}</div>
              </div>
              <div className={styles.tileValue}>{appVersion}</div>
            </button>

            <div className={styles.infoTile}>
              <div className={styles.tileHeader}>
                <div className={styles.tileLabel}>{t('footer.api_version')}</div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className={styles.tileAction}
                  onClick={() => void handleVersionCheck()}
                  loading={checkingVersion}
                  title={t('system_info.version_check_button')}
                  aria-label={t('system_info.version_check_button')}
                >
                  {t('system_info.version_check_button')}
                </Button>
              </div>
              <div className={styles.tileValue}>{apiVersion}</div>
            </div>

            <div className={styles.infoTile}>
              <div className={styles.tileLabel}>{t('footer.build_date')}</div>
              <div className={styles.tileValue}>{buildTime}</div>
            </div>

            <div className={styles.infoTile}>
              <div className={styles.tileLabel}>{t('connection.status')}</div>
              <div className={styles.tileValue}>{t(`common.${auth.connectionStatus}_status`)}</div>
              <div className={styles.tileSub}>{auth.apiBase || '-'}</div>
            </div>
          </div>
        </Card>

        <Card
          title="运行数据"
          extra={
            <div className={styles.runtimeToolbar}>
              <select
                className={styles.runtimeSelect}
                value={String(runtimeRefreshMs)}
                onChange={(event) => setRuntimeRefreshMs(Number(event.currentTarget.value))}
                aria-label="运行数据刷新周期"
              >
                {RUNTIME_REFRESH_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void fetchRuntimeMetrics()}
                loading={runtimeLoading}
              >
                立即刷新
              </Button>
            </div>
          }
        >
          {runtimeError && <div className="error-box">{runtimeError}</div>}
          <div className={styles.runtimeGrid}>
            <div className={styles.runtimeBlock}>
              <div className={styles.runtimeBlockTitle}>进程资源</div>
              <div className={styles.runtimeMetric}>
                <span>CPU</span>
                <strong>{formatPercent(runtimeMetrics?.process?.cpu_percent)}</strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>RSS/VMS</span>
                <strong>
                  {formatBytes(runtimeMetrics?.process?.rss_bytes)} /{' '}
                  {formatBytes(runtimeMetrics?.process?.vms_bytes)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>线程/FD</span>
                <strong>
                  {formatNumber(runtimeMetrics?.process?.threads)} /{' '}
                  {formatNumber(runtimeMetrics?.process?.fd_count)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>Goroutine</span>
                <strong>{formatNumber(runtimeMetrics?.process?.goroutines)}</strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>运行时长</span>
                <strong>{formatSeconds(runtimeMetrics?.process?.uptime_seconds)}</strong>
              </div>
            </div>

            <div className={styles.runtimeBlock}>
              <div className={styles.runtimeBlockTitle}>Go 内存与 GC</div>
              <div className={styles.runtimeMetric}>
                <span>HeapAlloc/Inuse</span>
                <strong>
                  {formatBytes(runtimeMetrics?.process?.heap_alloc)} /{' '}
                  {formatBytes(runtimeMetrics?.process?.heap_inuse)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>HeapSys/Released</span>
                <strong>
                  {formatBytes(runtimeMetrics?.process?.heap_sys)} /{' '}
                  {formatBytes(runtimeMetrics?.process?.heap_released)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>对象数/GC</span>
                <strong>
                  {formatNumber(runtimeMetrics?.process?.heap_objects)} /{' '}
                  {formatNumber(runtimeMetrics?.process?.gc_count)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>分配/释放</span>
                <strong>
                  {formatNumber(runtimeMetrics?.process?.mallocs)} /{' '}
                  {formatNumber(runtimeMetrics?.process?.frees)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>GC CPU/暂停</span>
                <strong>
                  {formatPercent(
                    typeof runtimeMetrics?.process?.gc_cpu_fraction === 'number'
                      ? runtimeMetrics.process.gc_cpu_fraction * 100
                      : 0
                  )}{' '}
                  / {formatNanoseconds(runtimeMetrics?.process?.gc_pause_total_ns)}
                </strong>
              </div>
            </div>

            <div className={styles.runtimeBlock}>
              <div className={styles.runtimeBlockTitle}>系统负载</div>
              <div className={styles.runtimeMetric}>
                <span>Load 1/5/15</span>
                <strong>
                  {[
                    runtimeMetrics?.system?.load1,
                    runtimeMetrics?.system?.load5,
                    runtimeMetrics?.system?.load15,
                  ]
                    .map((value) =>
                      typeof value === 'number' && Number.isFinite(value)
                        ? value.toFixed(2)
                        : '0.00'
                    )
                    .join(' / ')}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>Load/CPU</span>
                <strong>
                  {typeof runtimeMetrics?.system?.load1_per_cpu === 'number'
                    ? runtimeMetrics.system.load1_per_cpu.toFixed(2)
                    : '0.00'}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>系统内存</span>
                <strong>{formatPercent(runtimeMetrics?.system?.memory_used_percent)}</strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>可用内存</span>
                <strong>{formatBytes(runtimeMetrics?.system?.memory_available_bytes)}</strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>上下文切换</span>
                <strong>
                  {formatNumber(runtimeMetrics?.process?.voluntary_context_switches)} /{' '}
                  {formatNumber(runtimeMetrics?.process?.nonvoluntary_context_switches)}
                </strong>
              </div>
            </div>

            <div className={styles.runtimeBlock}>
              <div className={styles.runtimeBlockTitle}>认证与索引</div>
              <div className={styles.runtimeMetric}>
                <span>认证总数/Stub</span>
                <strong>
                  {formatNumber(runtimeMetrics?.auth?.auth_count)} /{' '}
                  {formatNumber(runtimeMetrics?.auth?.sqlite_stub_count)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>完整 Auth/LRU</span>
                <strong>
                  {formatNumber(runtimeMetrics?.auth?.full_auth_count)} /{' '}
                  {formatNumber(runtimeMetrics?.auth?.hydrated_cache_count)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>索引启用/可用</span>
                <strong>
                  {formatBoolean(runtimeMetrics?.auth_index?.enabled)} /{' '}
                  {formatBoolean(runtimeMetrics?.auth_index?.available)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>索引/payload 行</span>
                <strong>
                  {formatNumber(runtimeMetrics?.auth_index?.rows)} /{' '}
                  {formatNumber(runtimeMetrics?.auth_index?.payload_rows)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>连接等待/耗时</span>
                <strong>
                  {formatNumber(runtimeMetrics?.auth_index?.wait_count)} /{' '}
                  {formatNumber(runtimeMetrics?.auth_index?.wait_duration_ms)} ms
                </strong>
              </div>
            </div>

            <div className={styles.runtimeBlock}>
              <div className={styles.runtimeBlockTitle}>缓存与磁盘</div>
              <div className={styles.runtimeMetric}>
                <span>LRU/page-cache</span>
                <strong>
                  {formatNumber(runtimeMetrics?.auth_index?.lru_size)} /{' '}
                  {formatNumber(runtimeMetrics?.auth_index?.page_cache_kb)} KB
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>DB/WAL</span>
                <strong>
                  {formatBytes(runtimeMetrics?.auth_index?.db_bytes)} /{' '}
                  {formatBytes(runtimeMetrics?.auth_index?.wal_bytes)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>进程 IO</span>
                <strong>
                  {formatBytes(runtimeMetrics?.process?.io_read_bytes)} /{' '}
                  {formatBytes(runtimeMetrics?.process?.io_write_bytes)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>列表默认/上限</span>
                <strong>
                  {formatNumber(runtimeMetrics?.auth_index?.list_max_default)} /{' '}
                  {formatNumber(runtimeMetrics?.auth_index?.list_max_hard)}
                </strong>
              </div>
              <div className={styles.runtimeMetric}>
                <span>最后全量扫描</span>
                <strong>
                  {formatUnixSeconds(runtimeMetrics?.auth_index?.last_full_scan_unix, i18n.language)}
                </strong>
              </div>
            </div>
          </div>
          <div className={styles.runtimeFootnote}>
            更新时间：
            {runtimeMetrics?.timestamp
              ? formatDateTimeValue(runtimeMetrics.timestamp, i18n.language)
              : '-'}
          </div>
        </Card>

        <Card title={t('system_info.quick_links_title')}>
          <p className={styles.sectionDescription}>{t('system_info.quick_links_desc')}</p>
          <div className={styles.quickLinks}>
            <a
              href="https://github.com/router-for-me/CLIProxyAPI"
              target="_blank"
              rel="noopener noreferrer"
              className={styles.linkCard}
            >
              <div className={`${styles.linkIcon} ${styles.github}`}>
                <IconGithub size={22} />
              </div>
              <div className={styles.linkContent}>
                <div className={styles.linkTitle}>
                  {t('system_info.link_main_repo')}
                  <IconExternalLink size={14} />
                </div>
                <div className={styles.linkDesc}>{t('system_info.link_main_repo_desc')}</div>
              </div>
            </a>

            <a
              href="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
              target="_blank"
              rel="noopener noreferrer"
              className={styles.linkCard}
            >
              <div className={`${styles.linkIcon} ${styles.github}`}>
                <IconCode size={22} />
              </div>
              <div className={styles.linkContent}>
                <div className={styles.linkTitle}>
                  {t('system_info.link_webui_repo')}
                  <IconExternalLink size={14} />
                </div>
                <div className={styles.linkDesc}>{t('system_info.link_webui_repo_desc')}</div>
              </div>
            </a>

            <a
              href="https://help.router-for.me/"
              target="_blank"
              rel="noopener noreferrer"
              className={styles.linkCard}
            >
              <div className={`${styles.linkIcon} ${styles.docs}`}>
                <IconBookOpen size={22} />
              </div>
              <div className={styles.linkContent}>
                <div className={styles.linkTitle}>
                  {t('system_info.link_docs')}
                  <IconExternalLink size={14} />
                </div>
                <div className={styles.linkDesc}>{t('system_info.link_docs_desc')}</div>
              </div>
            </a>
          </div>
        </Card>

        <Card
          title={t('system_info.models_title')}
          extra={
            <Button
              variant="secondary"
              size="sm"
              onClick={() => fetchModels({ forceRefresh: true })}
              loading={modelsLoading}
            >
              {t('common.refresh')}
            </Button>
          }
        >
          <p className={styles.sectionDescription}>{t('system_info.models_desc')}</p>
          {modelStatus && (
            <div className={`status-badge ${modelStatus.type}`}>{modelStatus.message}</div>
          )}
          {modelsError && <div className="error-box">{modelsError}</div>}
          {modelsLoading ? (
            <div className="hint">{t('common.loading')}</div>
          ) : models.length === 0 ? (
            <div className="hint">{t('system_info.models_empty')}</div>
          ) : (
            <div className="item-list">
              {groupedModels.map((group) => {
                const iconSrc = getIconForCategory(group.id);
                return (
                  <div key={group.id} className="item-row">
                    <div className="item-meta">
                      <div className={styles.groupTitle}>
                        {iconSrc && <img src={iconSrc} alt="" className={styles.groupIcon} />}
                        <span className="item-title">{group.label}</span>
                      </div>
                      <div className="item-subtitle">
                        {t('system_info.models_count', { count: group.items.length })}
                      </div>
                    </div>
                    <div className={styles.modelTags}>
                      {group.items.map((model) => (
                        <span
                          key={`${model.name}-${model.alias ?? 'default'}`}
                          className={styles.modelTag}
                          title={model.description || ''}
                        >
                          <span className={styles.modelName}>{model.name}</span>
                          {model.alias && <span className={styles.modelAlias}>{model.alias}</span>}
                        </span>
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </Card>

        <Card title={t('system_info.clear_login_title')}>
          <p className={styles.sectionDescription}>{t('system_info.clear_login_desc')}</p>
          <div className={styles.clearLoginActions}>
            <Button variant="danger" onClick={handleClearLoginStorage}>
              {t('system_info.clear_login_button')}
            </Button>
          </div>
        </Card>
      </div>

      <Modal
        open={requestLogModalOpen}
        onClose={handleRequestLogClose}
        title={t('basic_settings.request_log_title')}
        footer={
          <>
            <Button variant="secondary" onClick={handleRequestLogClose} disabled={requestLogSaving}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={handleRequestLogSave}
              loading={requestLogSaving}
              disabled={!canEditRequestLog || !requestLogDirty}
            >
              {t('common.save')}
            </Button>
          </>
        }
      >
        <div className="request-log-modal">
          <div className="status-badge warning">{t('basic_settings.request_log_warning')}</div>
          <ToggleSwitch
            label={t('basic_settings.request_log_enable')}
            labelPosition="left"
            checked={requestLogDraft}
            disabled={!canEditRequestLog || requestLogSaving}
            onChange={(value) => {
              setRequestLogDraft(value);
              setRequestLogTouched(true);
            }}
          />
        </div>
      </Modal>
    </div>
  );
}
