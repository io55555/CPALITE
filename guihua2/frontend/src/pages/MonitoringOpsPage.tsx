import { useMemo } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { RequestLabPage } from '@/pages/RequestLabPage';
import { StatusRulerPage } from '@/pages/StatusRulerPage';
import { BreakerPage } from '@/pages/BreakerPage';
import styles from './InspectorPages.module.scss';

type OpsTab = 'capture' | 'rules' | 'breaker';

const TAB_CONFIG: Array<{ key: OpsTab; label: string; path: string }> = [
  { key: 'capture', label: '抓包 / 过滤', path: '/monitoring' },
  { key: 'rules', label: '状态规则', path: '/rules' },
  { key: 'breaker', label: 'IP 熔断', path: '/breaker' },
];

const detectTab = (pathname: string, search: string): OpsTab => {
  const tab = new URLSearchParams(search).get('tab');
  if (tab === 'rules') {
    return 'rules';
  }
  if (tab === 'breaker') {
    return 'breaker';
  }
  if (tab === 'capture') {
    return 'capture';
  }
  if (pathname === '/rules' || pathname === '/status-ruler') {
    return 'rules';
  }
  if (pathname === '/breaker') {
    return 'breaker';
  }
  return 'capture';
};

export function MonitoringOpsPage() {
  const location = useLocation();
  const navigate = useNavigate();
  const activeTab = detectTab(location.pathname, location.search);

  const content = useMemo(() => {
    if (activeTab === 'rules') return <StatusRulerPage />;
    if (activeTab === 'breaker') return <BreakerPage />;
    return <RequestLabPage />;
  }, [activeTab]);

  return (
    <div className={styles.page}>
      <div className={styles.opsHeader}>
        <div>
          <h1 className={styles.opsTitle}>请求监控</h1>
          <p className={styles.hint}>
            集中查看抓包记录、过滤调试、状态规则与账号代理 IP 熔断状态。
          </p>
        </div>
        <div className={styles.opsTabs}>
          {TAB_CONFIG.map((tab) => (
            <Button
              key={tab.key}
              size="sm"
              variant={activeTab === tab.key ? 'primary' : 'secondary'}
              onClick={() => navigate(tab.path)}
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
