import { useState } from 'react';
import { MonitoringCenterSsfunRequestPanel } from './MonitoringCenterSsfunRequestPanel';
import { MonitoringCenterSsfunInspectionPanel } from './MonitoringCenterSsfunInspectionPanel';
import styles from './MonitoringCenterSsfunPage.module.scss';

type SsfunTab = 'requests' | 'inspection';

export function MonitoringCenterSsfunPage() {
  const [activeTab, setActiveTab] = useState<SsfunTab>('requests');

  return (
    <div className={styles.page}>
      <div className={styles.tabs} role="tablist" aria-label="监控中心ssfun">
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'requests'}
          className={`${styles.tabButton} ${activeTab === 'requests' ? styles.tabButtonActive : ''}`}
          onClick={() => setActiveTab('requests')}
        >
          请求监控
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'inspection'}
          className={`${styles.tabButton} ${activeTab === 'inspection' ? styles.tabButtonActive : ''}`}
          onClick={() => setActiveTab('inspection')}
        >
          账号巡检
        </button>
      </div>
      <div className={styles.panel}>
        {activeTab === 'requests' ? (
          <MonitoringCenterSsfunRequestPanel />
        ) : (
          <MonitoringCenterSsfunInspectionPanel />
        )}
      </div>
    </div>
  );
}
