import { useEffect, useState } from 'react';
import styles from '@/pages/AiProvidersPage.module.scss';

export type ProviderDisplayMode = 'original' | 'table' | 'badge';

const STORAGE_PREFIX = 'cliproxyapi.aiProviders.displayMode.';
const DEFAULT_MODE: ProviderDisplayMode = 'table';

const MODE_OPTIONS: Array<{ value: ProviderDisplayMode; label: string }> = [
  { value: 'original', label: '原始样式' },
  { value: 'table', label: '紧凑表格' },
  { value: 'badge', label: '铭牌样式' },
];

const isProviderDisplayMode = (value: unknown): value is ProviderDisplayMode =>
  value === 'original' || value === 'table' || value === 'badge';

export function useProviderDisplayMode(providerId: string) {
  const storageKey = `${STORAGE_PREFIX}${providerId}`;
  const [mode, setMode] = useState<ProviderDisplayMode>(() => {
    if (typeof window === 'undefined') return DEFAULT_MODE;
    const stored = window.localStorage.getItem(storageKey);
    return isProviderDisplayMode(stored) ? stored : DEFAULT_MODE;
  });

  useEffect(() => {
    if (typeof window === 'undefined') return;
    window.localStorage.setItem(storageKey, mode);
  }, [mode, storageKey]);

  return { mode, setMode };
}

interface ProviderDisplayModeControlProps {
  value: ProviderDisplayMode;
  onChange: (value: ProviderDisplayMode) => void;
}

export function ProviderDisplayModeControl({
  value,
  onChange,
}: ProviderDisplayModeControlProps) {
  return (
    <span className={styles.providerModeControl} role="tablist" aria-label="显示样式">
      {MODE_OPTIONS.map((option) => (
        <button
          key={option.value}
          type="button"
          role="tab"
          aria-selected={value === option.value}
          className={`${styles.providerModeButton} ${
            value === option.value ? styles.providerModeButtonActive : ''
          }`}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </span>
  );
}
