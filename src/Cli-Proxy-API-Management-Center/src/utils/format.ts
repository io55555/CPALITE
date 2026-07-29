import { parseTimestamp } from './timestamp';

/**
 * 格式化工具函数
 * 从原项目 src/utils/string.js 迁移
 */

const resolveDefaultLocale = (): string | undefined => {
  const fromDocument =
    typeof document !== 'undefined' ? document.documentElement?.lang?.trim() : '';
  if (fromDocument) return fromDocument;
  const fromNavigator = typeof navigator !== 'undefined' ? navigator.language?.trim() : '';
  return fromNavigator || undefined;
};

const API_KEY_MASK_REGEX =
  /\b(?:sk-[A-Za-z0-9_-]{8,}|sk-ant-[A-Za-z0-9_-]{8,}|[A-Za-z0-9_-]{24,})\b/g;

/**
 * 隐藏 API Key 中间部分，仅保留前后两位
 */
export function maskApiKey(key: string): string {
  return maskApiKeyWithVisibleChars(key, 2);
}

export function maskApiKeyWithVisibleChars(key: string, visibleChars = 3): string {
  const trimmed = String(key || '').trim();
  if (!trimmed) {
    return '';
  }

  const MASKED_LENGTH = 10;
  const normalizedVisibleChars = Math.max(1, Math.min(visibleChars, Math.floor(trimmed.length / 2)));
  const start = trimmed.slice(0, normalizedVisibleChars);
  const end = trimmed.slice(-normalizedVisibleChars);
  const maskedLength = Math.max(MASKED_LENGTH - normalizedVisibleChars * 2, 1);
  const masked = '*'.repeat(maskedLength);

  return `${start}${masked}${end}`;
}

/**
 * 将文本中的 API Key 片段替换为脱敏显示
 */
export function maskSensitiveText(value: string): string {
  const trimmed = String(value || '').trim();
  if (!trimmed) return '';
  return trimmed.replace(API_KEY_MASK_REGEX, (match) => maskApiKey(match));
}

/**
 * 格式化文件大小
 */
export function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B';

  const units = ['B', 'KB', 'MB', 'GB'];
  const k = 1024;
  const i = Math.floor(Math.log(bytes) / Math.log(k));

  return `${(bytes / Math.pow(k, i)).toFixed(2)} ${units[i]}`;
}

const COMPACT_SUFFIXES = ['', 'K', 'M', 'B', 'T'] as const;

/**
 * 将较大的计数压缩为紧凑形式（1284 → 1.3K），用于统计卡片与图表标签
 */
export function formatCompactNumber(value: number): string {
  if (!Number.isFinite(value)) return '0';

  const sign = value < 0 ? '-' : '';
  let scaled = Math.abs(value);
  let tier = 0;

  while (scaled >= 1000 && tier < COMPACT_SUFFIXES.length - 1) {
    scaled /= 1000;
    tier += 1;
  }

  // 三位有效数字以内保留一位小数；Number() 顺带去掉 "1.0K" 这类冗余尾巴
  let rendered = tier === 0 ? Math.round(scaled) : Number(scaled.toFixed(scaled < 100 ? 1 : 0));

  // 四舍五入后又进位到 1000（如 999,999 → 1000K）时再升一档
  if (rendered >= 1000 && tier < COMPACT_SUFFIXES.length - 1) {
    rendered = 1;
    tier += 1;
  }

  return `${sign}${rendered}${COMPACT_SUFFIXES[tier]}`;
}

/**
 * 格式化百分比，去掉无意义的 ".0" 尾巴
 */
export function formatPercent(value: number, fractionDigits = 1): string {
  if (!Number.isFinite(value)) return '—';

  const rendered = value.toFixed(fractionDigits);
  return `${rendered.replace(/\.0+$/, '')}%`;
}

/**
 * 格式化日期时间
 */
export function formatDateTime(date: string | Date, locale?: string): string {
  const d = typeof date === 'string' ? parseTimestamp(date) ?? new Date(date) : date;

  if (isNaN(d.getTime())) {
    return 'Invalid Date';
  }

  const resolvedLocale = locale?.trim() || resolveDefaultLocale();
  return d.toLocaleString(resolvedLocale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  });
}

/**
 * 将 Unix 时间戳（秒/毫秒/微秒/纳秒）格式化为本地时间字符串
 */
export function formatUnixTimestamp(value: unknown, locale?: string): string {
  if (value === null || value === undefined || value === '') return '';

  const asNumber = typeof value === 'number' ? value : Number(value);
  const date = (() => {
    if (!Number.isFinite(asNumber) || Number.isNaN(asNumber)) {
      return parseTimestamp(value) ?? new Date(String(value));
    }

    const abs = Math.abs(asNumber);

    // 秒：常见 10 位（~1e9）
    if (abs < 1e11) return new Date(asNumber * 1000);

    // 毫秒：常见 13 位（~1e12）
    if (abs < 1e14) return new Date(asNumber);

    // 微秒：常见 16 位（~1e15）
    if (abs < 1e17) return new Date(Math.round(asNumber / 1000));

    // 纳秒：常见 19 位（~1e18）
    return new Date(Math.round(asNumber / 1e6));
  })();

  if (Number.isNaN(date.getTime())) return '';
  return locale ? date.toLocaleString(locale) : date.toLocaleString();
}

/**
 * 格式化数字（添加千位分隔符）
 */
export function formatNumber(num: number, locale?: string): string {
  const resolvedLocale = locale?.trim() || resolveDefaultLocale();
  return num.toLocaleString(resolvedLocale);
}

/**
 * 截断长文本
 */
export function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) {
    return text;
  }
  return text.slice(0, maxLength) + '...';
}

export function formatDateTimeValue(value: unknown, locale?: string): string {
  const parsed = parseTimestamp(value) ?? new Date(String(value ?? ''));
  return Number.isNaN(parsed.getTime()) ? '' : formatDateTime(parsed, locale);
}

export function formatDateValue(value: unknown, locale?: string): string {
  const parsed = parseTimestamp(value) ?? new Date(String(value ?? ''));
  if (Number.isNaN(parsed.getTime())) return '';
  const resolvedLocale = locale?.trim() || resolveDefaultLocale();
  return parsed.toLocaleDateString(resolvedLocale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  });
}
