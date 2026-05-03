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
      showNotification(error instanceof Error ? error.message : '状态规则加载失败', 'error');
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
      showNotification('规则已保存', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '保存规则失败', 'error');
    }
  };

  const deleteRule = async (id?: number) => {
    if (!id) return;
    try {
      await statusRulerApi.deleteRule(id);
      await load();
      showNotification('规则已删除', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '删除规则失败', 'error');
    }
  };

  return (
    <div className={styles.page}>
      <Card
        title="状态规则"
        extra={<Button size="sm" onClick={() => void load()} loading={loading}>刷新</Button>}
      >
        <div className={styles.ruleForm}>
          <Input label="规则名称" value={form.name} onChange={(event) => setForm((prev) => ({ ...prev, name: event.target.value }))} />
          <Input label="供应商" value={form.provider || ''} onChange={(event) => setForm((prev) => ({ ...prev, provider: event.target.value }))} />
          <Input label="认证索引" value={form.auth_index || ''} onChange={(event) => setForm((prev) => ({ ...prev, auth_index: event.target.value }))} />
          <Input
            label="状态码"
            type="number"
            value={String(form.status_code || 0)}
            onChange={(event) => setForm((prev) => ({ ...prev, status_code: Number(event.target.value) || 0 }))}
          />
          <Input
            label="响应包含文本"
            value={form.body_contains || ''}
            onChange={(event) => setForm((prev) => ({ ...prev, body_contains: event.target.value }))}
          />
          <Select
            value={form.action}
            options={[
              { value: 'log_only', label: '仅记录命中' },
              { value: 'breaker_open', label: '打开熔断' },
              { value: 'freeze_auth', label: '临时冻结认证' },
              { value: 'disable_auth', label: '停用认证' },
            ]}
            onChange={(value) => setForm((prev) => ({ ...prev, action: value }))}
            ariaLabel="规则动作"
          />
          <Input
            label="冷却秒数"
            type="number"
            value={String(form.cooldown_seconds || 0)}
            onChange={(event) => setForm((prev) => ({ ...prev, cooldown_seconds: Number(event.target.value) || 0 }))}
          />
          <ToggleSwitch
            checked={Boolean(form.enabled)}
            onChange={(value) => setForm((prev) => ({ ...prev, enabled: value }))}
            label="启用"
            ariaLabel="启用规则"
          />
        </div>
        <div className={styles.actions} style={{ marginTop: 12 }}>
          <Button size="sm" onClick={() => void saveRule()}>保存规则</Button>
          <Button size="sm" variant="secondary" onClick={() => setForm(emptyRule)}>重置表单</Button>
        </div>
      </Card>

      <Card title={`规则列表（${rules.length}）`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>名称</th>
              <th>匹配条件</th>
              <th>动作</th>
              <th>启用</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {rules.map((rule) => (
              <tr key={rule.id}>
                <td>{rule.name}</td>
                <td>{rule.provider || '*'} / {rule.auth_index || '*'} / {rule.status_code || '*'} / {rule.body_contains || '*'}</td>
                <td>{rule.action}</td>
                <td>{rule.enabled ? '是' : '否'}</td>
                <td className={styles.actions}>
                  <Button size="sm" variant="secondary" onClick={() => setForm(rule)}>编辑</Button>
                  <Button size="sm" variant="danger" onClick={() => void deleteRule(rule.id)}>删除</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      <Card title={`最近命中（${hits.length}）`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>时间</th>
              <th>规则</th>
              <th>供应商</th>
              <th>认证</th>
              <th>状态码</th>
              <th>动作</th>
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
