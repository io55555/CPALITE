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
            <h1 className={styles.opsTitle}>请求监控seakee</h1>
            <p className={styles.hint}>
              保留 seakee v1.0.4 风格的请求事件明细视图，并增强显示首字延迟、生成时间、TPS、思考强度和缓存命中。
            </p>
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
