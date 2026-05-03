import { useMemo } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { BreakerPage } from '@/pages/BreakerPage';
import { RequestLabPage } from '@/pages/RequestLabPage';
import { StatusRulerPage } from '@/pages/StatusRulerPage';
import styles from './InspectorPages.module.scss';

type OpsTab = 'capture' | 'rules' | 'breaker';

const TAB_CONFIG: Array<{ key: OpsTab; label: string }> = [
  { key: 'capture', label: '\u6293\u5305 / \u8fc7\u6ee4' },
  { key: 'rules', label: '\u72b6\u6001\u89c4\u5219' },
  { key: 'breaker', label: 'IP \u7194\u65ad' },
];

const normalizeTab = (value: string | null): OpsTab => {
  switch (value) {
    case 'rules':
      return 'rules';
    case 'breaker':
      return 'breaker';
    default:
      return 'capture';
  }
};

const detectTab = (pathname: string, search: string): OpsTab => {
  if (pathname === '/rules' || pathname === '/status-ruler') {
    return 'rules';
  }
  if (pathname === '/breaker') {
    return 'breaker';
  }
  return normalizeTab(new URLSearchParams(search).get('tab'));
};

const buildTabPath = (tab: OpsTab) => `/debugsetandip?tab=${tab}`;

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
          <h1 className={styles.opsTitle}>\u6293\u5305 / \u8fc7\u6ee4 / IP \u7194\u65ad</h1>
          <p className={styles.hint}>\u7edf\u4e00\u67e5\u770b\u6293\u5305\u8bb0\u5f55\u3001\u8fc7\u6ee4\u8c03\u8bd5\u3001\u72b6\u6001\u89c4\u5219\u548c\u8d26\u53f7\u4ee3\u7406 IP \u7194\u65ad\u72b6\u6001\u3002</p>
        </div>
        <div className={styles.opsTabs}>
          {TAB_CONFIG.map((tab) => (
            <Button
              key={tab.key}
              size="sm"
              variant={activeTab === tab.key ? 'primary' : 'secondary'}
              onClick={() => navigate(buildTabPath(tab.key))}
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
