import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import type { AuthRefreshQueueItem } from '@/types/authRefreshQueue';
import { parseTimestampMs } from '@/utils/timestamp';
import styles from '@/pages/CredentialCenterPage.module.scss';

const ONE_MINUTE_MS = 60 * 1000;
const TEN_MINUTES_MS = 10 * ONE_MINUTE_MS;
const ONE_HOUR_MS = 60 * ONE_MINUTE_MS;
const ONE_DAY_MS = 24 * ONE_HOUR_MS;
const SEVEN_DAYS_MS = 7 * ONE_DAY_MS;

type RefreshBucketId = 'within_1m' | 'within_10m' | 'within_1h' | 'within_1d' | 'within_7d' | 'longer';

interface RefreshBucketDefinition {
  id: RefreshBucketId;
  labelKey: string;
  maxMs: number;
  toneClass: string;
}

interface RefreshQueueEntry {
  item: AuthRefreshQueueItem;
  refreshAtMs: number;
  deltaMs: number;
  bucketId: RefreshBucketId;
}

interface AuthRefreshQueueCountdownCardProps {
  queue: AuthRefreshQueueItem[];
  loading: boolean;
  error: string | null;
  generatedAt?: string | null;
  onRefresh: () => void;
}

const getDisplayName = (item: AuthRefreshQueueItem): string =>
  item.name?.trim() || item.id?.trim() || item.auth_index?.trim() || '--';

const getBucketId = (deltaMs: number): RefreshBucketId => {
  if (deltaMs <= ONE_MINUTE_MS) return 'within_1m';
  if (deltaMs <= TEN_MINUTES_MS) return 'within_10m';
  if (deltaMs <= ONE_HOUR_MS) return 'within_1h';
  if (deltaMs <= ONE_DAY_MS) return 'within_1d';
  if (deltaMs <= SEVEN_DAYS_MS) return 'within_7d';
  return 'longer';
};

const formatClockTime = (timestampMs: number): string =>
  new Intl.DateTimeFormat(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  }).format(new Date(timestampMs));

export function AuthRefreshQueueCountdownCard({
  queue,
  loading,
  error,
  generatedAt,
  onRefresh
}: AuthRefreshQueueCountdownCardProps) {
  const { t } = useTranslation();
  const [now, setNow] = useState(() => Date.now());
  const [activeBucketId, setActiveBucketId] = useState<RefreshBucketId | null>(null);
  const bucketAreaRef = useRef<HTMLDivElement>(null);
  const detailsRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    if (!activeBucketId) return;

    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (detailsRef.current?.contains(target)) return;
      if (bucketAreaRef.current?.contains(target)) return;
      setActiveBucketId(null);
    };

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setActiveBucketId(null);
      }
    };

    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [activeBucketId]);

  const bucketDefinitions = useMemo<RefreshBucketDefinition[]>(
    () => [
      {
        id: 'within_1m',
        labelKey: 'credential_center.refresh_queue_bucket_1m',
        maxMs: ONE_MINUTE_MS,
        toneClass: styles.refreshQueueBucketDanger
      },
      {
        id: 'within_10m',
        labelKey: 'credential_center.refresh_queue_bucket_10m',
        maxMs: TEN_MINUTES_MS,
        toneClass: styles.refreshQueueBucketWarning
      },
      {
        id: 'within_1h',
        labelKey: 'credential_center.refresh_queue_bucket_1h',
        maxMs: ONE_HOUR_MS,
        toneClass: styles.refreshQueueBucketNotice
      },
      {
        id: 'within_1d',
        labelKey: 'credential_center.refresh_queue_bucket_1d',
        maxMs: ONE_DAY_MS,
        toneClass: styles.refreshQueueBucketInfo
      },
      {
        id: 'within_7d',
        labelKey: 'credential_center.refresh_queue_bucket_7d',
        maxMs: SEVEN_DAYS_MS,
        toneClass: styles.refreshQueueBucketCalm
      },
      {
        id: 'longer',
        labelKey: 'credential_center.refresh_queue_bucket_longer',
        maxMs: Number.POSITIVE_INFINITY,
        toneClass: styles.refreshQueueBucketMuted
      }
    ],
    []
  );

  const entries = useMemo<RefreshQueueEntry[]>(
    () =>
      queue
        .map((item) => {
          const refreshAtMs = parseTimestampMs(item.next_refresh_at);
          if (!Number.isFinite(refreshAtMs)) return null;
          const deltaMs = refreshAtMs - now;
          return {
            item,
            refreshAtMs,
            deltaMs,
            bucketId: getBucketId(deltaMs)
          };
        })
        .filter((entry): entry is RefreshQueueEntry => entry !== null)
        .sort((left, right) => left.refreshAtMs - right.refreshAtMs),
    [now, queue]
  );

  const buckets = useMemo(
    () =>
      bucketDefinitions.map((definition) => ({
        ...definition,
        entries: entries.filter((entry) => entry.bucketId === definition.id)
      })),
    [bucketDefinitions, entries]
  );

  const earliestEntry = entries[0] ?? null;
  const generatedAtMs = generatedAt ? parseTimestampMs(generatedAt) : Number.NaN;

  const formatDuration = useCallback(
    (durationMs: number): string => {
      if (durationMs <= 0) return t('credential_center.refresh_queue_due_now');

      const totalSeconds = Math.ceil(durationMs / 1000);
      const days = Math.floor(totalSeconds / 86400);
      const hours = Math.floor((totalSeconds % 86400) / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;

      if (days > 0) return `${days}${t('usage_stats.duration_unit_d')} ${hours}${t('usage_stats.duration_unit_h')}`;
      if (hours > 0) return `${hours}${t('usage_stats.duration_unit_h')} ${minutes}${t('usage_stats.duration_unit_m')}`;
      if (minutes > 0) return `${minutes}${t('usage_stats.duration_unit_m')} ${seconds}${t('usage_stats.duration_unit_s')}`;
      return `${seconds}${t('usage_stats.duration_unit_s')}`;
    },
    [t]
  );

  const formatDueLabel = useCallback(
    (durationMs: number): string =>
      durationMs <= 0
        ? t('credential_center.refresh_queue_due_now')
        : t('credential_center.refresh_queue_due_in', { time: formatDuration(durationMs) }),
    [formatDuration, t]
  );

  const toggleBucket = useCallback((bucketId: RefreshBucketId) => {
    setActiveBucketId((current) => (current === bucketId ? null : bucketId));
  }, []);

  return (
    <Card
      title={t('credential_center.refresh_queue_title')}
      className={styles.refreshQueueCard}
      extra={
        <div className={styles.refreshQueueHeaderMeta}>
          <span>{t('credential_center.refresh_queue_total', { count: entries.length })}</span>
          {Number.isFinite(generatedAtMs) && (
            <span>{t('credential_center.refresh_queue_snapshot', { time: formatClockTime(generatedAtMs) })}</span>
          )}
          <Button variant="secondary" size="sm" onClick={onRefresh} loading={loading}>
            {t('credential_center.refresh_queue_refresh')}
          </Button>
        </div>
      }
    >
      {loading && entries.length === 0 ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : entries.length === 0 && !error ? (
        <EmptyState
          title={t('credential_center.refresh_queue_empty_title')}
          description={t('credential_center.refresh_queue_empty_desc')}
        />
      ) : (
        <div className={styles.refreshQueueFloatingRoot}>
          <div className={styles.refreshQueueSummary}>
            <div className={styles.refreshQueueEarliest}>
              <span className={styles.refreshQueueEarliestLabel}>
                {t('credential_center.refresh_queue_earliest')}
              </span>
              <span className={styles.refreshQueueEarliestValue}>
                {earliestEntry ? formatDueLabel(earliestEntry.deltaMs) : '--'}
              </span>
              {earliestEntry && (
                <span className={styles.refreshQueueEarliestName}>{getDisplayName(earliestEntry.item)}</span>
              )}
            </div>

            <div className={styles.refreshQueueBuckets} ref={bucketAreaRef}>
              {buckets.map((bucket) => {
                const isActive = activeBucketId === bucket.id;
                const isEmpty = bucket.entries.length === 0;
                const bucketCellClassName = [
                  styles.refreshQueueBucketCell,
                  bucket.toneClass,
                  isEmpty ? styles.refreshQueueBucketEmpty : ''
                ]
                  .filter(Boolean)
                  .join(' ');
                const className = [
                  styles.refreshQueueBucket,
                  bucket.toneClass,
                  isActive ? styles.refreshQueueBucketActive : '',
                  isEmpty ? styles.refreshQueueBucketEmpty : ''
                ]
                  .filter(Boolean)
                  .join(' ');

                return (
                  <div key={bucket.id} className={bucketCellClassName}>
                    <button
                      type="button"
                      className={className}
                      onClick={() => toggleBucket(bucket.id)}
                      aria-pressed={isActive}
                    >
                      <span className={styles.refreshQueueBucketCount}>{bucket.entries.length}</span>
                      <span className={styles.refreshQueueBucketLabel}>{t(bucket.labelKey)}</span>
                    </button>
                    {isActive && (
                      <div className={styles.refreshQueueDetails} ref={detailsRef}>
                        <div className={styles.refreshQueueDetailsTitle}>
                          {t('credential_center.refresh_queue_details_title', {
                            bucket: t(bucket.labelKey),
                            count: bucket.entries.length
                          })}
                        </div>
                        {bucket.entries.length === 0 ? (
                          <div className={styles.hint}>{t('credential_center.refresh_queue_bucket_empty')}</div>
                        ) : (
                          <div className={styles.refreshQueueDetailList}>
                            {bucket.entries.map((entry) => (
                              <div key={`${entry.item.id}:${entry.item.auth_index}:${entry.item.next_refresh_at}`} className={styles.refreshQueueDetailRow}>
                                <div className={styles.refreshQueueDetailNameBlock}>
                                  <span className={styles.refreshQueueDetailName}>{getDisplayName(entry.item)}</span>
                                  <span className={styles.credentialType}>{entry.item.provider || '--'}</span>
                                </div>
                                <div className={styles.refreshQueueDetailTime}>{formatClockTime(entry.refreshAtMs)}</div>
                                <div className={styles.refreshQueueDetailCountdown}>{formatDuration(entry.deltaMs)}</div>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        </div>
      )}

      {error && <div className={styles.refreshQueueError}>{error}</div>}
    </Card>
  );
}
