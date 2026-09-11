import { useState } from 'react'
import { api } from '../api'

// 仓库接入表单：422 时展示缺失能力清单（AC01）。
export function RepoSetup() {
  const [form, setForm] = useState({ repo_numeric_id: '', owner: '', name: '', installation_id: '' })
  const [result, setResult] = useState<{ ok: boolean; msg: string; missing?: string[] } | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setResult(null)
    try {
      const resp = await api.req('/repositories', {
        method: 'POST',
        body: JSON.stringify({
          repo_numeric_id: Number(form.repo_numeric_id),
          owner: form.owner,
          name: form.name,
          installation_id: Number(form.installation_id),
        }),
      })
      if (resp.status === 422) {
        const body = await resp.json()
        setResult({ ok: false, msg: body.error ?? '能力检查未通过', missing: body.missing })
        return
      }
      if (!resp.ok) {
        setResult({ ok: false, msg: `${resp.status}: ${await resp.text()}` })
        return
      }
      setResult({ ok: true, msg: '接入成功' })
    } catch (err) {
      setResult({ ok: false, msg: String(err) })
    }
  }

  return (
    <form onSubmit={submit}>
      <h2>接入仓库</h2>
      {(
        [
          ['repo_numeric_id', '仓库数字 ID'],
          ['owner', 'owner'],
          ['name', 'name'],
          ['installation_id', 'installation ID'],
        ] as const
      ).map(([key, label]) => (
        <label key={key}>
          {label}
          <input
            value={form[key]}
            onChange={(e) => setForm({ ...form, [key]: e.target.value })}
            required={key !== 'repo_numeric_id' || true}
          />
        </label>
      ))}
      <button type="submit">实测能力并接入</button>
      {result && (
        <p className={result.ok ? 'ok' : 'err'}>
          {result.msg}
          {result.missing && (
            <>
              {' '}缺失能力：
              <ul>
                {result.missing.map((m) => (
                  <li key={m}>{m}</li>
                ))}
              </ul>
            </>
          )}
        </p>
      )}
    </form>
  )
}
