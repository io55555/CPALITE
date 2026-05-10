import { useMemo, type CSSProperties, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Line } from 'react-chartjs-2';
import {
  calculateRecentPerMinuteRates,
  calculateTotalCost,
  collectUsageDetails,
  formatPerMinuteValue,
  formatUsd,
  type ModelPrice,
  type UsageTimeRange
} from '@/utils/usage';
import { sparklineOptions } from '@/utils/usage/chartConfig';
import type { UsagePayload, SparklineBundle } from '@/components/usage';
import styles from '@/pages/MonitoringCenterPage.module.scss';

interface StatCardData {
  key: string;
  label: ReactNode;
  accent: string;
  accentSoft: string;
  accentBorder: string;
  value: ReactNode;
  trend: SparklineBundle | null;
}

export interface MonitorStatCardsProps {
  usage: UsagePayload | null;
  loading: boolean;
  modelPrices: Record<string, ModelPrice>;
  rateWindowMinutes: number;
  timeRange: UsageTimeRange;
  sparklines: {
    requests: SparklineBundle | null;
    tokens: SparklineBundle | null;
    rpm: SparklineBundle | null;
    tpm: SparklineBundle | null;
    cost: SparklineBundle | null;
  };
}

export function MonitorStatCards({
  usage,
  loading,
  modelPrices,
  rateWindowMinutes,
  timeRange,
  sparklines
}: MonitorStatCardsProps) {
  const { t } = useTranslation();

  const rateStats = useMemo(
    () => calculateRecentPerMinuteRates(rateWindowMinutes, usage),
    [rateWindowMinutes, usage]
  );
  const totalCost = useMemo(() => calculateTotalCost(usage, modelPrices), [usage, modelPrices]);
  const hasPrices = Object.keys(modelPrices).length > 0;
  const tokenBreakdown = useMemo(() => {
    const details = collectUsageDetails(usage);
    return details.reduce(
      (sum, detail) => {
        const input = Math.max(Number(detail.tokens?.input_tokens) || 0, 0);
        const output = Math.max(Number(detail.tokens?.output_tokens) || 0, 0);
        const cached = Math.max(
          Math.max(Number(detail.tokens?.cached_tokens) || 0, 0),
          Math.max(Number(detail.tokens?.cache_tokens) || 0, 0)
        );
        sum.input += input;
        sum.output += output;
        sum.cached += cached;
        return sum;
      },
      { input: 0, output: 0, cached: 0 }
    );
  }, [usage]);
  const cacheRatio =
    tokenBreakdown.input > 0 ? `${((tokenBreakdown.cached / tokenBreakdown.input) * 100).toFixed(2)}%` : '0.00%';
  const requestTotal = usage?.total_requests ?? 0;
  const successCount = usage?.success_count ?? 0;
  const failureCount = usage?.failure_count ?? 0;
  const successRate = requestTotal > 0 ? `${((successCount / requestTotal) * 100).toFixed(2)}%` : '0.00%';
  const tokenTotal = usage?.total_tokens ?? 0;
  const tokenPercent = (value: number) =>
    tokenTotal > 0 ? `${((value / tokenTotal) * 100).toFixed(2)}%` : '0.00%';
  const requestValue = (
    <span className={styles.statValueWithMeta}>
      <span>{loading ? '-' : requestTotal.toLocaleString()}</span>
      <span>
        (
        <span className={styles.statSuccessText}>成功{successCount.toLocaleString()}</span>
        {' '}
        <span className={styles.statFailureText}>失败{failureCount.toLocaleString()}</span>
        {' '}
        成功率{successRate}
        )
      </span>
    </span>
  );
  const tokenValue = (
    <span className={styles.statValueWithMeta}>
      <span>{loading ? '-' : tokenTotal.toLocaleString()}</span>
      <span>
        (
        输入{tokenBreakdown.input.toLocaleString()}
        {' '}
        <span className={styles.statSuccessText}>{tokenPercent(tokenBreakdown.input)}</span>
        {' '}
        输出{tokenBreakdown.output.toLocaleString()}
        {' '}
        <span className={styles.statFailureText}>{tokenPercent(tokenBreakdown.output)}</span>
        {' '}
        缓存{cacheRatio}
        )
      </span>
    </span>
  );

  const statsCards: StatCardData[] = [
    {
      key: 'requests',
      label: t('usage_stats.total_requests'),
      accent: '#8b8680',
      accentSoft: 'rgba(139, 134, 128, 0.18)',
      accentBorder: 'rgba(139, 134, 128, 0.35)',
      value: requestValue,
      trend: sparklines.requests
    },
    {
      key: 'tokens',
      label: '总Token数',
      accent: '#8b5cf6',
      accentSoft: 'rgba(139, 92, 246, 0.18)',
      accentBorder: 'rgba(139, 92, 246, 0.35)',
      value: tokenValue,
      trend: sparklines.tokens
    },
    {
      key: 'rpm',
      label: timeRange === 'all' ? t('usage_stats.rpm_30m') : 'RPM',
      accent: '#22c55e',
      accentSoft: 'rgba(34, 197, 94, 0.18)',
      accentBorder: 'rgba(34, 197, 94, 0.32)',
      value: loading ? '-' : formatPerMinuteValue(rateStats.rpm),
      trend: sparklines.rpm
    },
    {
      key: 'tpm',
      label: timeRange === 'all' ? t('usage_stats.tpm_30m') : 'TPM',
      accent: '#f97316',
      accentSoft: 'rgba(249, 115, 22, 0.18)',
      accentBorder: 'rgba(249, 115, 22, 0.32)',
      value: loading ? '-' : formatPerMinuteValue(rateStats.tpm),
      trend: sparklines.tpm
    },
    {
      key: 'cost',
      label: t('usage_stats.total_cost'),
      accent: '#f59e0b',
      accentSoft: 'rgba(245, 158, 11, 0.18)',
      accentBorder: 'rgba(245, 158, 11, 0.32)',
      value: loading ? '-' : hasPrices ? formatUsd(totalCost) : '--',
      trend: hasPrices ? sparklines.cost : null
    }
  ];

  return (
    <div className={styles.statsGrid}>
      {statsCards.map((card) => (
        <div
          key={card.key}
          className={styles.statCard}
          style={
            {
              '--accent': card.accent,
              '--accent-soft': card.accentSoft,
              '--accent-border': card.accentBorder
            } as CSSProperties
          }
        >
          <div className={styles.statCardHeader}>
            <span className={styles.statLabel}>{card.label}</span>
          </div>
          <div className={styles.statValue}>{card.value}</div>
          <div className={styles.statTrend}>
            {card.trend ? (
              <Line className={styles.sparkline} data={card.trend.data} options={sparklineOptions} />
            ) : (
              <div className={styles.statTrendPlaceholder}></div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
