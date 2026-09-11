import { useState } from 'react'
import { api } from './api'
import { CaseList } from './pages/CaseList'
import { CaseDetailPage } from './pages/CaseDetail'
import { RepoSetup } from './pages/RepoSetup'

// M1 工作台壳：接入 / 列表 / 详情 三视图 + 操作者令牌输入。
export default function App() {
  const [view, setView] = useState<'cases' | 'setup'>('cases')
  const [caseId, setCaseId] = useState<string | null>(null)
  const [tokenInput, setTokenInput] = useState(api.token)

  return (
    <main style={{ fontFamily: 'system-ui, sans-serif', padding: '2rem', maxWidth: 960 }}>
      <header style={{ display: 'flex', gap: '1rem', alignItems: 'center', marginBottom: '1rem' }}>
        <h1 style={{ margin: 0 }}>DevFlow 工作台</h1>
        <nav>
          <button onClick={() => { setView('cases'); setCaseId(null) }}>Case 列表</button>
          <button onClick={() => setView('setup')}>仓库接入</button>
        </nav>
        <span style={{ marginLeft: 'auto' }}>
          操作者令牌：
          <input
            type="password"
            value={tokenInput}
            onChange={(e) => setTokenInput(e.target.value)}
            onBlur={() => api.setToken(tokenInput)}
            placeholder="OPERATOR_TOKEN"
          />
        </span>
      </header>

      {view === 'setup' && <RepoSetup />}
      {view === 'cases' && !caseId && <CaseList onOpen={setCaseId} />}
      {view === 'cases' && caseId && (
        <CaseDetailPage caseId={caseId} onBack={() => setCaseId(null)} />
      )}
    </main>
  )
}
