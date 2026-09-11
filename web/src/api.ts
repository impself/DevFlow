// API 客户端：所有请求走同源 /api（Vite dev 代理到 :8080），
// 操作者令牌放 localStorage——M1 单操作者的最简凭证管理。
export const api = {
  token: localStorage.getItem('devflow_operator_token') ?? '',

  setToken(t: string) {
    this.token = t
    localStorage.setItem('devflow_operator_token', t)
  },

  async req(path: string, init?: RequestInit): Promise<Response> {
    const resp = await fetch(`/api${path}`, {
      ...init,
      headers: {
        'Content-Type': 'application/json',
        'X-Operator-Token': this.token,
        ...init?.headers,
      },
    })
    return resp
  },

  async json<T>(path: string, init?: RequestInit): Promise<T> {
    const resp = await this.req(path, init)
    if (!resp.ok && resp.status !== 422) {
      throw new Error(`${resp.status}: ${await resp.text()}`)
    }
    return resp.json()
  },
}

export interface CaseSummary {
  id: string
  issue_number: number
  title: string
  state: string
  updated_at: string
  repo: { owner: string; name: string; issue_url: string }
}

export interface CaseDetail extends CaseSummary {
  runs: RunSummary[]
  draft?: {
    id: string
    conclusion: string
    evidence: Array<{
      source_type: string
      location: { path: string; sha: string; line_start?: number; line_end?: number }
      quote?: string
    }>
    needs_info_questions?: string[]
    cost_cny_total?: number
    model_calls?: number
  }
}

export interface RunSummary {
  id: string
  status: string
  outcome: string | null
  created_at: string
  finished_at: string | null
  error: string | null
}

export interface RunDetail {
  run: RunSummary & { attempts: number; model_calls_used: number }
  commits: Array<{ commit_id: string; stage: string; created_at: string; result: unknown }>
  model_calls: Array<{ model: string; input_tokens: number; output_tokens: number; cost_cny: number; cost_status: string }>
}
