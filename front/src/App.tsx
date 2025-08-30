import { useMemo, useState } from 'react'
import './App.css'

type Player = { gameName: string; tagLine: string }
type TeamPlayer = { name: string; skill_score: number; main_lanes?: string[] }
type AnalyzeResponse = {
  teamA: TeamPlayer[]
  teamB: TeamPlayer[]
  sumA: number
  sumB: number
}

const API_BASE = import.meta.env.VITE_API_BASE || 'http://localhost:8080'
const MAX_PLAYERS = 10

function App() {
  const [players, setPlayers] = useState<Player[]>([])
  const [matchLimit, setMatchLimit] = useState<number>(10)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<AnalyzeResponse | null>(null)
  const [info, setInfo] = useState<string | null>(null)
  const [lobbyLog, setLobbyLog] = useState<string>('')
  const [newPlayer, setNewPlayer] = useState<Player>({ gameName: '', tagLine: '' })

  const canSubmit = useMemo(() => players.length >= 2 && !loading, [players, loading])

  const removePlayer = (idx: number) => setPlayers(prev => prev.filter((_, i) => i !== idx))

  const submit = async () => {
    setLoading(true)
    setError(null)
    setInfo(null)
    setResult(null)
    try {
      const body = { players, matchLimit }
      const res = await fetch(`${API_BASE}/analyze`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error(await res.text())
      const data: AnalyzeResponse = await res.json()
      setResult(data)
    } catch (e: any) {
      setError(e?.message || 'request failed')
    } finally {
      setLoading(false)
    }
  }

  // Parse players from pasted lobby logs like: "ふぇいかー#JP1がロビーに参加しました"
  const parsePlayersFromLog = (text: string): Player[] => {
    const out: Player[] = []
    const seen = new Set<string>()
    const lines = text.split(/\r?\n/)
    for (const line of lines) {
      // Support formats like: "名前#タグがロビーに参加しました" or plain "名前#タグ"
      // Capture until the join phrase, whitespace, or end-of-line (handles 日本語タグ e.g. 百鬼組)
      const rx = /\s*(.+?)#(.+?)(?=がロビーに参加しました|\s|$)/
      const m = line.match(rx)
      if (!m) continue
      const gameName = m[1].trim()
      const tagLine = m[2].trim().toUpperCase()
      if (!gameName || !tagLine) continue
      const key = `${gameName}#${tagLine}`
      if (!seen.has(key)) {
        out.push({ gameName, tagLine })
        seen.add(key)
      }
    }
    return out
  }

  const mergeUnique = (base: Player[], add: Player[]): Player[] => {
    const set = new Set(base.map(p => `${p.gameName}#${p.tagLine.toUpperCase()}`))
    const res = [...base]
    for (const p of add) {
      const key = `${p.gameName}#${p.tagLine.toUpperCase()}`
      if (!set.has(key)) {
        res.push(p)
        set.add(key)
      }
    }
    return res
  }

  const registerFromLog = () => {
    const parsed = parsePlayersFromLog(lobbyLog)
    if (parsed.length === 0) {
      setError('ログからプレイヤー名を検出できませんでした')
      return
    }
    setPlayers(prev => {
      const before = prev.length
      const merged = mergeUnique(prev, parsed)
      const added = Math.min(merged.length, MAX_PLAYERS) - before
      setInfo(`ログから${added}人を追加しました（${Math.min(merged.length, MAX_PLAYERS)}/${MAX_PLAYERS}）`)
      setError(null)
      return merged.slice(0, MAX_PLAYERS)
    })
  }

  const addNewPlayer = () => {
    const gn = newPlayer.gameName.trim()
    const tl = newPlayer.tagLine.trim()
    if (!gn || !tl) return
    setPlayers(prev => {
      if (prev.length >= MAX_PLAYERS) return prev
      const keySet = new Set(prev.map(p => `${p.gameName}#${p.tagLine.toUpperCase()}`))
      const key = `${gn}#${tl.toUpperCase()}`
      if (keySet.has(key)) {
        setError('すでに登録済みです')
        return prev
      }
      setError(null)
      setInfo('1人追加しました')
      return [...prev, { gameName: gn, tagLine: tl.toUpperCase() }]
    })
    setNewPlayer({ gameName: '', tagLine: '' })
  }

  return (
    <div style={{ padding: 16, maxWidth: 900, margin: '0 auto' }}>
      <h1>LOL Skill Match – Analyzer</h1>
      <p>プレイヤー名(GameName)とタグ(TagLine)を入力して解析します。</p>
      <div style={{ marginBottom: 12 }}>
        <label>Match Limit: </label>
        <input
          type="number"
          value={matchLimit}
          min={1}
          onChange={(e) => setMatchLimit(parseInt(e.target.value || '0'))}
        />
      </div>
      <div style={{ marginBottom: 8 }}>登録人数: {players.length}/{MAX_PLAYERS}</div>
      <div style={{ marginBottom: 12 }}>
        <label>ロビーのログを貼り付け → 「登録」を押す:</label>
        <textarea
          value={lobbyLog}
          onChange={e => setLobbyLog(e.target.value)}
          placeholder={`例)\nふぇいかー#JP1がロビーに参加しました\nしょうめいかー#JP1がロビーに参加しました\nたーざん#JP1がロビーに参加しました`}
          rows={4}
          style={{ width: '100%' }}
        />
        <div style={{ marginTop: 8 }}>
          <button onClick={registerFromLog}>登録</button>
        </div>
      </div>

      {/* Registered players (read-only rows) */}
      <div style={{ marginBottom: 12 }}>
        {players.map((p, i) => (
          <div key={i} style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 6 }}>
            <div style={{ flex: 1 }}>{p.gameName}</div>
            <div style={{ width: 120, textAlign: 'center' }}>{p.tagLine}</div>
            <button onClick={() => removePlayer(i)}>削除</button>
          </div>
        ))}
      </div>

      {/* Single input row for adding a player (hidden when 10/10) */}
      {players.length < MAX_PLAYERS && (
        <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
          <input
            placeholder="GameName"
            value={newPlayer.gameName}
            onChange={e => setNewPlayer(prev => ({ ...prev, gameName: e.target.value }))}
          />
          <input
            placeholder="TagLine"
            value={newPlayer.tagLine}
            onChange={e => setNewPlayer(prev => ({ ...prev, tagLine: e.target.value }))}
          />
          <button onClick={addNewPlayer} disabled={!newPlayer.gameName || !newPlayer.tagLine}>行を追加</button>
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
        <button onClick={submit} disabled={!canSubmit}>{loading ? '解析中...' : '解析'}</button>
      </div>
      {error && <div style={{ color: 'red' }}>{error}</div>}
      {info && <div style={{ color: 'green' }}>{info}</div>}

      {result && (
        <div style={{ display: 'flex', gap: 24 }}>
          <div style={{ flex: 1 }}>
            <h2>Team A (合計: {result.sumA})</h2>
            <ul>
              {result.teamA.map((p, idx) => (
                <li key={idx}>
                  {p.name} – スキル: {p.skill_score} {p.main_lanes?.length ? ` / レーン: ${p.main_lanes.join(',')}` : ''}
                </li>
              ))}
            </ul>
          </div>
          <div style={{ flex: 1 }}>
            <h2>Team B (合計: {result.sumB})</h2>
            <ul>
              {result.teamB.map((p, idx) => (
                <li key={idx}>
                  {p.name} – スキル: {p.skill_score} {p.main_lanes?.length ? ` / レーン: ${p.main_lanes.join(',')}` : ''}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}
    </div>
  )
}

export default App
