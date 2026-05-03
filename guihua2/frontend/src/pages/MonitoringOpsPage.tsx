import { Button } from '@/components/ui/Button';
import { BreakerPage } from '@/pages/BreakerPage';
import { RequestLabPage } from '@/pages/RequestLabPage';
import { StatusRulerPage } from '@/pages/StatusRulerPage';
import { useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import styles from './InspectorPages.module.scss';

type OpsTab = 'capture' | 'rules' | 'breaker';

const TAB_CONFIG: Array<{ key: OpsTab; label: string }> = [
  { key: 'capture', label: '抓包 / 过滤' },
  { key: 'rules', label: '状态规则' },
  { key: 'breaker', label: 'IP 熔断' },
];

const normalizeTab = (value: string | null): OpsTab => {
  if (value === 'rules') return 'rules';
  if (value === 'breaker') return 'breaker';
  return 'capture';
};

export function MonitoringOpsPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const activeTab = normalizeTab(searchParams.get('tab'));

  const content = useMemo(() => {
    if (activeTab === 'rules') return <StatusRulerPage key="rules" />;
    if (activeTab === 'breaker') return <BreakerPage key="breaker" />;
    return <RequestLabPage key="capture" />;
  }, [activeTab]);

  const switchTab = (tab: OpsTab) => {
    navigate({
      pathname: '/debugsetandip',
      search: `?tab=${tab}`,
    });
  };

  return (
    <div className={styles.page}>
      <div className={styles.opsHeader}>
        <div>
          <h1 className={styles.opsTitle}>抓包 / 过滤 / IP 熔断</h1>
          <p className={styles.hint}>
            统一查看抓包记录、过滤调试、状态规则和账号级代理 IP 熔断状态。
          </p>
        </div>
        <div className={styles.opsTabs}>
          {TAB_CONFIG.map((tab) => (
            <Button
              key={tab.key}
              size="sm"
              variant={activeTab === tab.key ? 'primary' : 'secondary'}
              onClick={() => switchTab(tab.key)}
            >
              {tab.label}
            </Button>
          ))}
        </div>
      </div>
      {content}
    </div>
  );
}
