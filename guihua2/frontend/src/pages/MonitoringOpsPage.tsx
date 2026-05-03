import { Button } from '@/components/ui/Button';
import { BreakerPage } from '@/pages/BreakerPage';
import { RequestLabPage } from '@/pages/RequestLabPage';
import { StatusRulerPage } from '@/pages/StatusRulerPage';
import { useMemo } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import styles from './InspectorPages.module.scss';

type OpsTab = 'capture' | 'rules' | 'breaker';

const TAB_CONFIG: Array<{ key: OpsTab; label: string; path: string }> = [
  { key: 'capture', label: '抓包 / 过滤', path: '/debugsetandip' },
  { key: 'rules', label: '状态规则', path: '/debugsetandip/rules' },
  { key: 'breaker', label: 'IP 熔断', path: '/debugsetandip/breaker' },
];

const detectTab = (pathname: string): OpsTab => {
  if (pathname.startsWith('/debugsetandip/rules')) return 'rules';
  if (pathname.startsWith('/debugsetandip/breaker')) return 'breaker';
  return 'capture';
};

export function MonitoringOpsPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const activeTab = detectTab(location.pathname);

  const content = useMemo(() => {
    if (activeTab === 'rules') return <StatusRulerPage key="rules" />;
    if (activeTab === 'breaker') return <BreakerPage key="breaker" />;
    return <RequestLabPage key="capture" />;
  }, [activeTab]);

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
