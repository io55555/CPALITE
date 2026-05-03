import { useCallback, useEffect, useState } from 'react';
import { Card } from '@/components/ui/Card';
import { RequestEventsDetailsCard, useUsageData } from '@/components/usage';
import { authFilesApi } from '@/services/api/authFiles';
import { useConfigStore } from '@/stores';
import type { AuthFileItem } from '@/types/authFile';
import styles from './InspectorPages.module.scss';

export function SeakeeMonitoringPage() {
  const config = useConfigStore((state) => state.config);
  const [authFiles, setAuthFiles] = useState<AuthFileItem[]>([]);
  const { usage, loading, error, lastRefreshedAt, loadUsage } = useUsageData({ timeRange: '24h' });

  const loadAuthFiles = useCallback(async () => {
    const res = await authFilesApi.list();
    const files = Array.isArray(res) ? res : (res as { files?: AuthFileItem[] })?.files;
    setAuthFiles(Array.isArray(files) ? files : []);
  }, []);

  const handleRefresh = useCallback(async () => {
    await Promise.all([loadUsage(), loadAuthFiles()]);
  }, [loadAuthFiles, loadUsage]);

  useEffect(() => {
    void loadAuthFiles();
  }, [loadAuthFiles]);

  return (
    <div className={styles.page}>
      <Card>
        <div className={styles.opsHeader}>
          <div>
            <h1 className={styles.opsTitle}>\u8bf7\u6c42\u76d1\u63a7seakee</h1>
            <p className={styles.hint}>\u4fdd\u7559 seakee v1.0.4 \u98ce\u683c\u7684\u8bf7\u6c42\u4e8b\u4ef6\u76d1\u63a7\u89c6\u56fe\uff0c\u7528\u4e8e\u6309\u6a21\u578b\u3001\u6765\u6e90\u3001\u8ba4\u8bc1\u7d22\u5f15\u7b5b\u9009\u8bf7\u6c42\u660e\u7ec6\u3002</p>
            {error ? <p className={styles.hint}>{error}</p> : null}
          </div>
        </div>
      </Card>

      <RequestEventsDetailsCard
        usage={usage}
        loading={loading}
        geminiKeys={config?.geminiApiKeys || []}
        claudeConfigs={config?.claudeApiKeys || []}
        codexConfigs={config?.codexApiKeys || []}
        vertexConfigs={config?.vertexApiKeys || []}
        openaiProviders={config?.openaiCompatibility || []}
        authFiles={authFiles}
        onRefresh={handleRefresh}
        lastRefreshedAt={lastRefreshedAt}
      />
    </div>
  );
}
