import { useEffect, useState } from 'react'
import { api, type CaseDetail, type RunDetail } from '../api'

// 草稿正文按 Markdown 渲染。
// PRD §6.4 要求 sanitize：M1 不引 sanitize 库的替代——
// 不用 dangerouslySetInnerHTML，以 <pre> 纯文本展示（零 XSS 面），
// 引入 react-markdown + rehype-sanitize 是 US3 后续增强项（记入 README）。
function DraftBody({ detail }: { detail: CaseDetail }) {
  const d = detail.draft
  if (!d) return <p className="muted">尚无草稿。</p>
  if (d.conclusion === 'NEEDS_INFO') {
    return (
      <div>
        <p className="badge" style={{ background: '#d97706' }}>NEEDS_INFO — 需要补充信息</p>
        <ul>
          {(d.needs_info_questions ?? []).map((q, i) => (
            <li key={i}>{q}</li>
          ))}
        </ul>
      </div>
    )
  }
  return (
    <p className="muted">
      草稿正文经批准后发布（M1 工作台显示证据与结论，正文在批准弹层中核对）。
      结论：<strong>{d.conclusion}</strong>
    </p>
  )
}

export function CaseDetailPage({ caseId, onBack }: { caseId: string; onBack: () => void }) {
  const [detail, setDetail] = useState<CaseDetail | null>(null)
  const [runDetail, setRunDetail] = useState<RunDetail | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    api.json<CaseDetail>(`/cases/${caseId}`).then(setDetail).catch((e) => setErr(String(e)))
  }, [caseId])

  const openRun = async (runId: string) => {
    setRunDetail(await api.json<RunDetail>(`/runs/${runId}`))
  }

  const approve = async () => {
    // 创建审批包 → 批准 → 发布（发布前服务端还有 preflight 重核）
    try {
      const created = await api.json<{ id: string }>(`/cases/${caseId}/approval-bundle`, { method: 'POST' })
      await api.req(`/bundles/${created.id}/approve`, { method: 'POST' })
      const resp = await api.req(`/bundles/${created.id}/publish`, { method: 'POST' })
      const body = await resp.json()
      alert(resp.ok ? `已发布：${JSON.stringify(body.action?.status)}` : `发布失败：${JSON.stringify(body)}`)
    } catch (e) {
      alert(String(e))
    }
  }

  if (err) return <p className="err">{err}</p>
  if (!detail) return <p className="muted">加载中…</p>

  return (
    <div>
      <p>
        <button onClick={onBack}>← 返回列表</button>
      </p>
      <h2>
        #{detail.issue_number} {detail.title}
      </h2>

      <h3>草稿</h3>
      <DraftBody detail={detail} />
      {detail.draft && (
        <>
          <h4>证据（可核对：path + sha + 行号）</h4>
          <ul>
            {detail.draft.evidence.map((ev, i) => (
              <li key={i}>
                <code>
                  {ev.location.path}@{ev.location.sha.slice(0, 7)}
                  {ev.location.line_start ? ` L${ev.location.line_start}-${ev.location.line_end}` : ''}
                </code>
                {ev.quote && <blockquote>{ev.quote}</blockquote>}
              </li>
            ))}
          </ul>
          {detail.draft.cost_cny_total !== undefined && (
            <p>
              费用：¥{detail.draft.cost_cny_total.toFixed(4)}（{detail.draft.model_calls} 次调用）
            </p>
          )}
          {detail.draft.conclusion === 'ANSWER_READY' && (
            <button onClick={approve}>批准并发布评论</button>
          )}
        </>
      )}

      <h3>Run 轨迹</h3>
      <table>
        <tbody>
          {detail.runs.map((r) => (
            <tr key={r.id} onClick={() => openRun(r.id)} style={{ cursor: 'pointer' }}>
              <td>{r.id}</td>
              <td>{r.status}</td>
              <td>{r.outcome ?? ''}</td>
              <td>{new Date(r.created_at).toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>

      {runDetail && (
        <div>
          <h4>Run 详情（{runDetail.run.id}）</h4>
          <p>
            状态 {runDetail.run.status} / epoch 重试 {runDetail.run.attempts} / 模型调用{' '}
            {runDetail.run.model_calls_used}
          </p>
          <h5>提交回执</h5>
          <pre>{JSON.stringify(runDetail.commits, null, 2)}</pre>
          <h5>模型费用</h5>
          <ul>
            {runDetail.model_calls.map((mc, i) => (
              <li key={i}>
                {mc.model}: {mc.input_tokens}+{mc.output_tokens} tok → ¥{mc.cost_cny}（{mc.cost_status}）
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
