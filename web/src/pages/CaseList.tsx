import { useEffect, useState } from 'react'
import { api, type CaseSummary } from '../api'

// 状态徽标颜色（PRD §6.4 的状态子集）。
const stateStyle: Record<string, string> = {
  analyzing: '#2563eb',
  waiting_info: '#d97706',
  awaiting_approval: '#7c3aed',
  replied: '#16a34a',
  closed: '#6b7280',
}

export function CaseList({ onOpen }: { onOpen: (id: string) => void }) {
  const [cases, setCases] = useState<CaseSummary[]>([])
  const [err, setErr] = useState('')

  useEffect(() => {
    api
      .json<{ cases: CaseSummary[] }>('/cases')
      .then((d) => setCases(d.cases))
      .catch((e) => setErr(String(e)))
  }, [])

  if (err) return <p className="err">加载失败：{err}</p>
  if (cases.length === 0) return <p className="muted">暂无 Case——在测试仓库创建 Issue 触发。</p>

  return (
    <table>
      <thead>
        <tr>
          <th>Issue</th>
          <th>标题</th>
          <th>状态</th>
          <th>更新时间</th>
        </tr>
      </thead>
      <tbody>
        {cases.map((c) => (
          <tr key={c.id} onClick={() => onOpen(c.id)} style={{ cursor: 'pointer' }}>
            <td>
              <a href={c.repo.issue_url} target="_blank" rel="noreferrer">
                {c.repo.owner}/{c.repo.name}#{c.issue_number}
              </a>
            </td>
            <td>{c.title}</td>
            <td>
              <span className="badge" style={{ background: stateStyle[c.state] ?? '#6b7280' }}>
                {c.state}
              </span>
            </td>
            <td>{new Date(c.updated_at).toLocaleString()}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
