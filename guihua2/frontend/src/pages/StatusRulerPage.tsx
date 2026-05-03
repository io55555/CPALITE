import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { statusRulerApi, type StatusRule, type StatusRuleHit } from '@/services/api';
import { useNotificationStore } from '@/stores';
import styles from './InspectorPages.module.scss';

const emptyRule: StatusRule = {
  name: '',
  enabled: true,
  provider: '',
  auth_index: '',
  status_code: 0,
  body_contains: '',
  action: 'log_only',
  cooldown_seconds: 120,
};

export function StatusRulerPage() {
  const { showNotification } = useNotificationStore();
  const [rules, setRules] = useState<StatusRule[]>([]);
  const [hits, setHits] = useState<StatusRuleHit[]>([]);
  const [form, setForm] = useState<StatusRule>(emptyRule);
  const [loading, setLoading] = useState(true);

  const load = async () => {
    setLoading(true);
    try {
      const [rulesResp, hitsResp] = await Promise.all([
        statusRulerApi.listRules(),
        statusRulerApi.listHits(100),
      ]);
      setRules(rulesResp.items);
      setHits(hitsResp.items);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Status Ruler load failed', 'error');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const saveRule = async () => {
    try {
      await statusRulerApi.saveRule(form);
      setForm(emptyRule);
      await load();
      showNotification('Rule saved', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to save rule', 'error');
    }
  };

  const deleteRule = async (id?: number) => {
    if (!id) return;
    try {
      await statusRulerApi.deleteRule(id);
      await load();
      showNotification('Rule deleted', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to delete rule', 'error');
    }
  };

  return (
    <div className={styles.page}>
      <Card
        title="Status Ruler"
        extra={<Button size="sm" onClick={() => void load()} loading={loading}>Refresh</Button>}
      >
        <div className={styles.ruleForm}>
          <Input label="Name" value={form.name} onChange={(event) => setForm((prev) => ({ ...prev, name: event.target.value }))} />
          <Input label="Provider" value={form.provider || ''} onChange={(event) => setForm((prev) => ({ ...prev, provider: event.target.value }))} />
          <Input label="Auth Index" value={form.auth_index || ''} onChange={(event) => setForm((prev) => ({ ...prev, auth_index: event.target.value }))} />
          <Input
            label="Status Code"
            type="number"
            value={String(form.status_code || 0)}
            onChange={(event) => setForm((prev) => ({ ...prev, status_code: Number(event.target.value) || 0 }))}
          />
          <Input
            label="Body Contains"
            value={form.body_contains || ''}
            onChange={(event) => setForm((prev) => ({ ...prev, body_contains: event.target.value }))}
          />
          <Select
            value={form.action}
            options={[
              { value: 'log_only', label: 'Log only' },
              { value: 'breaker_open', label: 'Open breaker' },
              { value: 'freeze_auth', label: 'Freeze auth' },
              { value: 'disable_auth', label: 'Disable auth' },
            ]}
            onChange={(value) => setForm((prev) => ({ ...prev, action: value }))}
            ariaLabel="Rule action"
          />
          <Input
            label="Cooldown Seconds"
            type="number"
            value={String(form.cooldown_seconds || 0)}
            onChange={(event) => setForm((prev) => ({ ...prev, cooldown_seconds: Number(event.target.value) || 0 }))}
          />
          <ToggleSwitch
            checked={Boolean(form.enabled)}
            onChange={(value) => setForm((prev) => ({ ...prev, enabled: value }))}
            label="Enabled"
            ariaLabel="Rule enabled"
          />
        </div>
        <div className={styles.actions} style={{ marginTop: 12 }}>
          <Button size="sm" onClick={() => void saveRule()}>Save Rule</Button>
          <Button size="sm" variant="secondary" onClick={() => setForm(emptyRule)}>Reset Form</Button>
        </div>
      </Card>

      <Card title={`Rules (${rules.length})`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Match</th>
              <th>Action</th>
              <th>Enabled</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            {rules.map((rule) => (
              <tr key={rule.id}>
                <td>{rule.name}</td>
                <td>{rule.provider || '*'} / {rule.auth_index || '*'} / {rule.status_code || '*'} / {rule.body_contains || '*'}</td>
                <td>{rule.action}</td>
                <td>{rule.enabled ? 'Yes' : 'No'}</td>
                <td className={styles.actions}>
                  <Button size="sm" variant="secondary" onClick={() => setForm(rule)}>Edit</Button>
                  <Button size="sm" variant="danger" onClick={() => void deleteRule(rule.id)}>Delete</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      <Card title={`Recent Hits (${hits.length})`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Time</th>
              <th>Rule</th>
              <th>Provider</th>
              <th>Auth</th>
              <th>Status</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            {hits.map((hit) => (
              <tr key={hit.id}>
                <td>{hit.created_at}</td>
                <td>{hit.rule_name}</td>
                <td>{hit.provider || '-'}</td>
                <td>{hit.auth_index || hit.auth_id || '-'}</td>
                <td>{hit.status_code || '-'}</td>
                <td>{hit.action}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
