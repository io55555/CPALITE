import { Navigate, useRoutes, type Location } from 'react-router-dom';
import { DashboardPage } from '@/pages/DashboardPage';
import { ProvidersWorkbenchPage } from '@/features/providers/ProvidersWorkbenchPage';
import { AiProvidersPage } from '@/pages/AiProvidersPage';
import { AiProvidersAmpcodeEditPage } from '@/pages/AiProvidersAmpcodeEditPage';
import { AiProvidersClaudeEditLayout } from '@/pages/AiProvidersClaudeEditLayout';
import { AiProvidersClaudeEditPage } from '@/pages/AiProvidersClaudeEditPage';
import { AiProvidersClaudeModelsPage } from '@/pages/AiProvidersClaudeModelsPage';
import { AiProvidersCodexEditPage } from '@/pages/AiProvidersCodexEditPage';
import { AiProvidersGeminiEditPage } from '@/pages/AiProvidersGeminiEditPage';
import { AiProvidersOpenAIEditLayout } from '@/pages/AiProvidersOpenAIEditLayout';
import { AiProvidersOpenAIEditPage } from '@/pages/AiProvidersOpenAIEditPage';
import { AiProvidersOpenAIModelsPage } from '@/pages/AiProvidersOpenAIModelsPage';
import { AiProvidersVertexEditPage } from '@/pages/AiProvidersVertexEditPage';
import { AuthFilesPage } from '@/pages/AuthFilesPage';
import { AuthFilesOAuthExcludedEditPage } from '@/pages/AuthFilesOAuthExcludedEditPage';
import { AuthFilesOAuthModelAliasEditPage } from '@/pages/AuthFilesOAuthModelAliasEditPage';
import { OAuthPage } from '@/pages/OAuthPage';
import { QuotaPage } from '@/pages/QuotaPage';
import { MonitoringCenterPage } from '@/pages/MonitoringCenterPage';
import { PacketCapturePage } from '@/pages/PacketCapturePage';
import { MonitoringCenterPage as MonitoringCenterSeakeePage } from '@/pages/MonitoringCenterSeakeePage';
import { CodexInspectionPage as CodexInspectionSeakeePage } from '@/pages/CodexInspectionSeakeePage';
import { MonitoringCenterSsfunPage } from '@/pages/MonitoringCenterSsfunPage';
import { ConfigPage } from '@/pages/ConfigPage';
import { LogsPage } from '@/pages/LogsPage';
import { SystemPage } from '@/pages/SystemPage';

const mainRoutes = [
  { path: '/', element: <DashboardPage /> },
  { path: '/dashboard', element: <DashboardPage /> },
  { path: '/settings', element: <Navigate to="/config" replace /> },
  { path: '/api-keys', element: <Navigate to="/config" replace /> },
  { path: '/ai-providers', element: <ProvidersWorkbenchPage /> },
  { path: '/ai-providers/*', element: <Navigate to="/ai-providers" replace /> },
  { path: '/ai-providers-class', element: <AiProvidersPage /> },
  { path: '/ai-providers-class/gemini/new', element: <AiProvidersGeminiEditPage /> },
  { path: '/ai-providers-class/gemini/:index', element: <AiProvidersGeminiEditPage /> },
  { path: '/ai-providers-class/codex/new', element: <AiProvidersCodexEditPage /> },
  { path: '/ai-providers-class/codex/:index', element: <AiProvidersCodexEditPage /> },
  {
    path: '/ai-providers-class/claude/new',
    element: <AiProvidersClaudeEditLayout />,
    children: [
      { index: true, element: <AiProvidersClaudeEditPage /> },
      { path: 'models', element: <AiProvidersClaudeModelsPage /> },
    ],
  },
  {
    path: '/ai-providers-class/claude/:index',
    element: <AiProvidersClaudeEditLayout />,
    children: [
      { index: true, element: <AiProvidersClaudeEditPage /> },
      { path: 'models', element: <AiProvidersClaudeModelsPage /> },
    ],
  },
  { path: '/ai-providers-class/vertex/new', element: <AiProvidersVertexEditPage /> },
  { path: '/ai-providers-class/vertex/:index', element: <AiProvidersVertexEditPage /> },
  { path: '/ai-providers-class/ampcode', element: <AiProvidersAmpcodeEditPage /> },
  {
    path: '/ai-providers-class/openai/new',
    element: <AiProvidersOpenAIEditLayout />,
    children: [
      { index: true, element: <AiProvidersOpenAIEditPage /> },
      { path: 'models', element: <AiProvidersOpenAIModelsPage /> },
    ],
  },
  {
    path: '/ai-providers-class/openai/:index',
    element: <AiProvidersOpenAIEditLayout />,
    children: [
      { index: true, element: <AiProvidersOpenAIEditPage /> },
      { path: 'models', element: <AiProvidersOpenAIModelsPage /> },
    ],
  },
  { path: '/auth-files', element: <AuthFilesPage /> },
  { path: '/auth-files/oauth-excluded', element: <AuthFilesOAuthExcludedEditPage /> },
  { path: '/auth-files/oauth-model-alias', element: <AuthFilesOAuthModelAliasEditPage /> },
  { path: '/oauth', element: <OAuthPage /> },
  { path: '/quota', element: <QuotaPage /> },
  { path: '/monitor', element: <MonitoringCenterPage /> },
  { path: '/packet-capture', element: <PacketCapturePage /> },
  { path: '/monitorseakee', element: <MonitoringCenterSeakeePage /> },
  { path: '/monitorseakee/codex-inspection', element: <CodexInspectionSeakeePage /> },
  { path: '/monitorssfun', element: <MonitoringCenterSsfunPage /> },
  { path: '/credential-center', element: <Navigate to="/monitor" replace /> },
  { path: '/config', element: <ConfigPage /> },
  { path: '/logs', element: <LogsPage /> },
  { path: '/system', element: <SystemPage /> },
  { path: '*', element: <Navigate to="/" replace /> },
];

export function MainRoutes({ location }: { location?: Location }) {
  return useRoutes(mainRoutes, location);
}
