package main

import (
    "encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// Tier/Rankを数値化するマップ
var tierToInt = map[string]int{
	"IRON":        1,
	"BRONZE":      2,
	"SILVER":      3,
	"GOLD":        4,
	"PLATINUM":    5,
	"EMERALD":     6,
	"DIAMOND":     7,
	"MASTER":      8,
	"GRANDMASTER": 9,
	"CHALLENGER":  10,
}
var intToTier = map[int]string{
	1:  "IRON",
	2:  "BRONZE",
	3:  "SILVER",
	4:  "GOLD",
	5:  "PLATINUM",
	6:  "EMERALD",
	7:  "DIAMOND",
	8:  "MASTER",
	9:  "GRANDMASTER",
	10: "CHALLENGER",
}
var rankToInt = map[string]int{
	"IV":  1,
	"III": 2,
	"II":  3,
	"I":   4,
}
var intToRank = map[int]string{
	1: "IV",
	2: "III",
	3: "II",
	4: "I",
}

// Tier/Rank/LPを一意のスコアに変換
func rankScore(tier, rank string, lp int) int {
	t := tierToInt[tier]
	r := rankToInt[rank]
	return ((t-1)*4+(r-1))*100 + lp
}

// スコアからTier/Rank/LPに逆変換
func scoreToRank(score int) (string, string, int) {
	tierIdx := score/400 + 1
	rankIdx := (score%400)/100 + 1
	lp := score % 100
	tier := intToTier[tierIdx]
	rank := intToRank[rankIdx]
	return tier, rank, lp
}

type Account struct {
	PUUID    string `json:"puuid"`
	GameName string `json:"gameName"`
	TagLine  string `json:"tagLine"`
}

type Player struct {
	GameName string `json:"gameName"`
	TagLine  string `json:"tagLine"`
}

// -------- レートリミット/進捗管理 --------
type RiotLimiter struct {
	mu     sync.Mutex
	secWin []time.Time
	twoMin []time.Time
}

func NewRiotLimiter() *RiotLimiter { return &RiotLimiter{} }

// Wait blocks until a request is permitted under 20req/s and 100req/120s.
// Returns total sleep time spent inside the call.
func (r *RiotLimiter) Wait() time.Duration {
	var slept time.Duration
	for {
		r.mu.Lock()
		now := time.Now()
		// prune windows
		cutoff1 := now.Add(-1 * time.Second)
		for len(r.secWin) > 0 && r.secWin[0].Before(cutoff1) {
			r.secWin = r.secWin[1:]
		}
		cutoff2 := now.Add(-120 * time.Second)
		for len(r.twoMin) > 0 && r.twoMin[0].Before(cutoff2) {
			r.twoMin = r.twoMin[1:]
		}
		// if allowed now
		if len(r.secWin) < 20 && len(r.twoMin) < 100 {
			// record send time
			r.secWin = append(r.secWin, now)
			r.twoMin = append(r.twoMin, now)
			r.mu.Unlock()
			return slept
		}
		// compute sleep needed to satisfy both limits
		wait1 := time.Duration(0)
		if len(r.secWin) >= 20 {
			w := r.secWin[0].Add(1 * time.Second).Sub(now)
			if w > wait1 {
				wait1 = w
			}
		}
		wait2 := time.Duration(0)
		if len(r.twoMin) >= 100 {
			w := r.twoMin[0].Add(120 * time.Second).Sub(now)
			if w > wait2 {
				wait2 = w
			}
		}
		sleepFor := wait1
		if wait2 > sleepFor {
			sleepFor = wait2
		}
		if sleepFor < 10*time.Millisecond {
			sleepFor = 10 * time.Millisecond
		}
		r.mu.Unlock()
		time.Sleep(sleepFor)
		slept += sleepFor
	}
}

type Counters struct {
	mu        sync.Mutex
	players   int
	planned   int
	attempts  int
	completed int
	retries   int
	start     time.Time
	waitRL    time.Duration
	wait429   time.Duration
}

func NewCounters(players int) *Counters {
	return &Counters{players: players, start: time.Now()}
}
func (c *Counters) AddPlanned(n int) {
	c.mu.Lock()
	c.planned += n
	c.mu.Unlock()
}
func (c *Counters) RecordAttempt() {
	c.mu.Lock()
	c.attempts++
	c.mu.Unlock()
}
func (c *Counters) RecordCompleted() {
	c.mu.Lock()
	c.completed++
	c.mu.Unlock()
}
func (c *Counters) RecordRetry() {
	c.mu.Lock()
	c.retries++
	c.mu.Unlock()
}
func (c *Counters) AddRateWait(d time.Duration) {
	if d > 0 {
		c.mu.Lock()
		c.waitRL += d
		c.mu.Unlock()
	}
}
func (c *Counters) Add429Wait(d time.Duration) {
	if d > 0 {
		c.mu.Lock()
		c.wait429 += d
		c.mu.Unlock()
	}
}
func (c *Counters) Snapshot() (players, planned, attempts, completed, retries int, elapsed time.Duration, eta time.Duration, waitRL, wait429 time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	players = c.players
	planned = c.planned
	attempts = c.attempts
	completed = c.completed
	retries = c.retries
	elapsed = time.Since(c.start)
	waitRL = c.waitRL
	wait429 = c.wait429
	remain := planned - completed
	if remain < 0 {
		remain = 0
	}
	// Based on application limit 100 requests / 120 seconds => 1.2s per request
	eta = time.Duration(float64(remain) * 1.2 * float64(time.Second))
	return
}
func durStr(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", mins, secs)
}
func (c *Counters) PrintEstimate(prefix string) {
	p, pl, at, cm, rt, el, eta, wrl, w429 := c.Snapshot()
	note := ""
	if prefix != "" {
		note = " - " + prefix
	}
	fmt.Printf("[進捗] プレイヤー:%d 完了:%d/%d (試行:%d/リトライ:%d) 経過:%s 待機(制限/429):%s/%s 予想残り:%s%s\n",
		p, cm, pl, at, rt, durStr(el), durStr(wrl), durStr(w429), durStr(eta), note)
}

// 改良版リトライ付きAPIリクエスト（429はRetry-Afterに従い無制限リトライ）
func doRequestWithRetry(req *http.Request, client *http.Client, limiter *RiotLimiter, counters *Counters, maxRetry int) (*http.Response, error) {
	// SKIPフラグ取得
	skipOnLimit := os.Getenv("SKIP") == "true"

	backoff := 1 * time.Second
	var lastStatus int
	tries := 0
	for {
		// Acquire under rate limits (メイン側でETA表示)
		slept := limiter.Wait()
		counters.AddRateWait(slept)
		counters.RecordAttempt()
		resp, err := client.Do(req)
		tries++
		if err == nil && resp != nil && resp.StatusCode == 200 {
			counters.RecordCompleted()
			return resp, nil
		}
		if resp != nil {
			lastStatus = resp.StatusCode
			// 404は正常扱い（アンランク等）
			if resp.StatusCode == 404 {
				counters.RecordCompleted()
				return resp, nil
			}
			// 429: Retry-Afterに従って必ずリトライ
			if resp.StatusCode == 429 {
				counters.RecordRetry()
				ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
				resp.Body.Close()
				var wait time.Duration
				if ra != "" {
					if v, err := strconv.Atoi(ra); err == nil {
						wait = time.Duration(v) * time.Second
					}
				}
				if wait == 0 {
					// Fallback: 2分窓のペース配分に合わせる
					wait = 2 * time.Second
				}
				fmt.Printf("[情報] 429 Too Many Requests: %s 待機\n", durStr(wait))
				counters.Add429Wait(wait)
				if skipOnLimit {
					// SKIP=trueなら無視して次へ
					return nil, nil
				}
				time.Sleep(wait)
				continue // 無制限リトライ
			}
			// 一時的なサーバーエラー（5xx）は指数バックオフでリトライ
			if resp.StatusCode >= 500 && resp.StatusCode < 600 {
				resp.Body.Close()
				if skipOnLimit {
					return nil, nil
				}
				if maxRetry > 0 && tries >= maxRetry {
					break
				}
				time.Sleep(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			// それ以外のステータスはエラー扱い
			resp.Body.Close()
		}
		// ネットワークエラー等
		if skipOnLimit {
			return nil, nil
		}
		if maxRetry > 0 && tries >= maxRetry {
			break
		}
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil, fmt.Errorf("APIリクエスト失敗（リトライ上限, status=%d）", lastStatus)
}

func main() {
	godotenv.Load()
	apiKey := os.Getenv("RIOT_API_KEY")
	if apiKey == "" {
		log.Fatal("RIOT_API_KEYが設定されていません")
	}

	// 複数プレイヤー対応: プレイヤー名リストをJSONから読み込み
	playersPath := os.Getenv("PLAYERS_FILE")
	if playersPath == "" {
		playersPath = "players.json" // backend直下を想定
	}
	var players []Player
	if b, err := os.ReadFile(playersPath); err != nil {
		log.Fatalf("プレイヤーリストJSON読込失敗 (%s): %v", playersPath, err)
	} else {
		if err := json.Unmarshal(b, &players); err != nil {
			log.Fatalf("プレイヤーリストJSONパース失敗 (%s): %v", playersPath, err)
		}
	}
	if len(players) == 0 {
		log.Fatalf("プレイヤーリストが空です (%s)", playersPath)
	}

	// レートリミット/進捗管理の初期化
	limiter := NewRiotLimiter()
	counters := NewCounters(len(players))
	// 概算の案内
	matchLimit := 10
	if ml := os.Getenv("MATCH_LIMIT"); ml != "" {
		if n, err := strconv.Atoi(ml); err == nil && n > 0 {
			matchLimit = n
		}
	}
	approxPerPlayer := 4 + 12*matchLimit // account(1), matchlist(1), matchdetail*2(matchLimit*2), rank(1), mastery(1), participants rank(~matchLimit*10)
	fmt.Printf("対象プレイヤー数: %d\n", len(players))
	fmt.Printf("レート制限: 20 req/s, 100 req/120s (理論最大≒50 req/分)\n")
	fmt.Printf("MATCH_LIMIT: %d\n", matchLimit)
	fmt.Printf("1人あたり想定Riotリクエスト(概算): %d 件\n", approxPerPlayer)
	fmt.Printf("理論最短所要時間(概算): 約 %.1f 分\n", float64(approxPerPlayer*len(players))*1.2/60.0)

	var allPlayerData []map[string]interface{} // AI用データ格納
	// メインgoroutineで進捗を表示するため、処理本体は別goroutineで実行
	done := make(chan struct{})
	go func() {

		for _, player := range players {
			fmt.Printf("\n==== %s#%s のデータ取得開始 ====\n", player.GameName, player.TagLine)
			fmt.Printf("[開始] %s#%s: アカウント情報取得\n", player.GameName, player.TagLine)
			gameName := player.GameName // ゲーム名
			tagLine := player.TagLine   // タグライン

			url := fmt.Sprintf("https://asia.api.riotgames.com/riot/account/v1/accounts/by-riot-id/%s/%s", gameName, tagLine)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				log.Fatal(err)
			}
			req.Header.Set("X-Riot-Token", apiKey)

			client := &http.Client{}
			counters.AddPlanned(1) // account by riot-id
			resp, err := doRequestWithRetry(req, client, limiter, counters, 3)
			if err != nil {
				log.Fatalf("APIリクエスト失敗: %v", err)
			}
			if resp == nil {
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				log.Fatalf("APIリクエスト失敗: %s", resp.Status)
			}

			var account Account
			if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
				log.Fatal(err)
			}

			fmt.Printf("ゲーム名: %s#%s\nPUUID: %s\n", account.GameName, account.TagLine, account.PUUID)

			// 2. PUUIDからマッチIDリストを取得
			fmt.Printf("[開始] %s#%s: マッチリスト取得\n", player.GameName, player.TagLine)
			matchListUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/by-puuid/%s/ids?start=0&count=100", account.PUUID)
			matchReq, err := http.NewRequest("GET", matchListUrl, nil)
			if err != nil {
				log.Fatal(err)
			}
			matchReq.Header.Set("X-Riot-Token", apiKey)

			counters.AddPlanned(1) // match list
			matchResp, err := doRequestWithRetry(matchReq, client, limiter, counters, 3)
			if err != nil {
				log.Fatalf("マッチリストAPIリクエスト失敗: %v", err)
			}
			if matchResp == nil {
				continue
			}
			defer matchResp.Body.Close()

			if matchResp.StatusCode != 200 {
				log.Fatalf("マッチリストAPIリクエスト失敗: %s", matchResp.Status)
			}

			var matchIDs []string
			if err := json.NewDecoder(matchResp.Body).Decode(&matchIDs); err != nil {
				log.Fatal(err)
			}

			fmt.Printf("取得したマッチID数: %d\n", len(matchIDs))
			for i, id := range matchIDs {
				fmt.Printf("%d: %s\n", i+1, id)
			}

			// 3. 各マッチIDから詳細を取得し、使ったチャンピオンを集計
			championCount := make(map[int]int)
			laneCount := make(map[string]int) // レーン集計用
			maxMatches := 10                  // デフォルト: 10試合分集計
			if ml := os.Getenv("MATCH_LIMIT"); ml != "" {
				if n, err := strconv.Atoi(ml); err == nil && n > 0 {
					maxMatches = n
				}
			}
			if len(matchIDs) < maxMatches {
				maxMatches = len(matchIDs)
			}
			// ランク戦回数・勝利数
			rankedCount := 0
			rankedWin := 0
			fmt.Printf("[開始] %s#%s: マッチ詳細(使用チャンプ/レーン) 取得 %d件\n", player.GameName, player.TagLine, maxMatches)
			// 使うマッチ詳細(1回目)
			counters.AddPlanned(maxMatches)
			for i := 0; i < maxMatches; i++ {
				matchID := matchIDs[i]
				matchDetailUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", matchID)
				matchDetailReq, err := http.NewRequest("GET", matchDetailUrl, nil)
				if err != nil {
					log.Fatal(err)
				}
				matchDetailReq.Header.Set("X-Riot-Token", apiKey)

				matchDetailResp, err := doRequestWithRetry(matchDetailReq, client, limiter, counters, 3)
				if err != nil {
					log.Fatalf("マッチ詳細APIリクエスト失敗: %v", err)
				}
				if matchDetailResp == nil {
					continue
				}
				defer matchDetailResp.Body.Close()

				if matchDetailResp.StatusCode != 200 {
					log.Printf("マッチ詳細APIリクエスト失敗: %s", matchDetailResp.Status)
					continue
				}

				var matchDetail struct {
					Info struct {
						QueueID      int `json:"queueId"`
						Participants []struct {
							PUUID        string `json:"puuid"`
							ChampionID   int    `json:"championId"`
							TeamPosition string `json:"teamPosition"`
							Win          bool   `json:"win"`
						} `json:"participants"`
					} `json:"info"`
				}
				if err := json.NewDecoder(matchDetailResp.Body).Decode(&matchDetail); err != nil {
					log.Printf("マッチ詳細デコード失敗: %v", err)
					continue
				}

				// アリーナ(1700), クイックプレイ(490), ARAM(450)は無視
				if matchDetail.Info.QueueID == 1700 || matchDetail.Info.QueueID == 490 || matchDetail.Info.QueueID == 450 {
					continue
				}
				// ノーマル(400, 430)とランク(420)のみ集計
				if matchDetail.Info.QueueID != 400 && matchDetail.Info.QueueID != 430 && matchDetail.Info.QueueID != 420 {
					continue
				}

				for _, p := range matchDetail.Info.Participants {
					if p.PUUID == account.PUUID {
						championCount[p.ChampionID]++
						lane := p.TeamPosition
						if lane == "" {
							lane = "UNKNOWN"
						}
						laneCount[lane]++
						// ランク戦判定
						if matchDetail.Info.QueueID == 420 {
							rankedCount++
							if p.Win {
								rankedWin++
							}
						}
					}
				}
				// API制限対策（RiotLimiterで吸収）
			}

			// Data DragonからチャンピオンID→名前のマップを取得
			championIDToName := make(map[int]string)
			championDataURL := "https://ddragon.leagueoflegends.com/cdn/15.14.1/data/ja_JP/champion.json"
			championResp, err := http.Get(championDataURL)
			if err != nil {
				log.Printf("チャンピオンデータ取得失敗: %v", err)
			} else {
				defer championResp.Body.Close()
				var champData struct {
					Data map[string]struct {
						Key  string `json:"key"`
						Name string `json:"name"`
					} `json:"data"`
				}
				if err := json.NewDecoder(championResp.Body).Decode(&champData); err != nil {
					log.Printf("チャンピオンデータデコード失敗: %v", err)
				} else {
					for _, v := range champData.Data {
						// keyはstring型の数字
						var id int
						fmt.Sscanf(v.Key, "%d", &id)
						championIDToName[id] = v.Name
					}
				}
			}

			// 4. チャンピオンIDごとに多い順で出力
			fmt.Println("\n使ったチャンピオンランキング（多い順）:")
			type champStat struct {
				ID    int
				Count int
			}
			var stats []champStat
			for id, cnt := range championCount {
				stats = append(stats, champStat{ID: id, Count: cnt})
			}
			// 降順ソート
			sort.Slice(stats, func(i, j int) bool {
				return stats[i].Count > stats[j].Count
			})
			for _, s := range stats {
				name := championIDToName[s.ID]
				if name == "" {
					name = "不明"
				}
				fmt.Printf("%s (ID: %d), 回数: %d\n", name, s.ID, s.Count)
			}

			// レーン集計結果を多い順で出力
			fmt.Println("\n担当したレーン回数（多い順）:")
			type laneStat struct {
				Lane  string
				Count int
			}
			var laneStats []laneStat
			for lane, cnt := range laneCount {
				laneStats = append(laneStats, laneStat{Lane: lane, Count: cnt})
			}
			sort.Slice(laneStats, func(i, j int) bool {
				return laneStats[i].Count > laneStats[j].Count
			})
			for _, s := range laneStats {
				fmt.Printf("%s: %d回\n", s.Lane, s.Count)
			}

			// ランク情報取得（by-puuid版）
			fmt.Printf("[開始] %s#%s: ランク情報取得\n", player.GameName, player.TagLine)
			rankUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/by-puuid/%s", account.PUUID)
			rankReq, err := http.NewRequest("GET", rankUrl, nil)
			if err != nil {
				log.Fatal(err)
			}
			rankReq.Header.Set("X-Riot-Token", apiKey)

			counters.AddPlanned(1) // rank (by puuid)
			rankResp, err := doRequestWithRetry(rankReq, client, limiter, counters, 3)
			if err != nil {
				log.Fatalf("ランク情報取得APIリクエスト失敗: %v", err)
			}
			if rankResp == nil {
				continue
			}
			defer rankResp.Body.Close()

			if rankResp.StatusCode != 200 {
				log.Fatalf("ランク情報取得APIリクエスト失敗: %s", rankResp.Status)
			}

			var rankData []struct {
				QueueType    string `json:"queueType"`
				Tier         string `json:"tier"`
				Rank         string `json:"rank"`
				LeaguePoints int    `json:"leaguePoints"`
			}
			if err := json.NewDecoder(rankResp.Body).Decode(&rankData); err != nil {
				log.Fatal(err)
			}

			fmt.Println("\nランク情報:")
			found := false
			for _, entry := range rankData {
				if entry.QueueType == "RANKED_SOLO_5x5" {
					fmt.Printf("ソロランク: %s %s %dLP\n", entry.Tier, entry.Rank, entry.LeaguePoints)
					found = true
				}
			}
			if !found {
				fmt.Println("ソロランク: ランクなし")
			}

			// マスタリーAPI取得（by-puuid版）
			fmt.Printf("[開始] %s#%s: マスタリー取得\n", player.GameName, player.TagLine)
			masteryUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/champion-mastery/v4/champion-masteries/by-puuid/%s", account.PUUID)
			masteryReq, err := http.NewRequest("GET", masteryUrl, nil)
			if err != nil {
				log.Fatal(err)
			}
			masteryReq.Header.Set("X-Riot-Token", apiKey)

			counters.AddPlanned(1) // mastery (by puuid)
			masteryResp, err := doRequestWithRetry(masteryReq, client, limiter, counters, 3)
			if err != nil {
				log.Fatalf("マスタリーAPIリクエスト失敗: %v", err)
			}
			if masteryResp == nil {
				continue
			}
			defer masteryResp.Body.Close()

			if masteryResp.StatusCode != 200 {
				log.Fatalf("マスタリーAPIリクエスト失敗: %s", masteryResp.Status)
			}

			var masteries []struct {
				ChampionID     int `json:"championId"`
				ChampionLevel  int `json:"championLevel"`
				ChampionPoints int `json:"championPoints"`
			}
			if err := json.NewDecoder(masteryResp.Body).Decode(&masteries); err != nil {
				log.Fatal(err)
			}

			fmt.Println("\nチャンピオンマスタリー:")
			for _, m := range masteries {
				name := championIDToName[m.ChampionID]
				if name == "" {
					name = "不明"
				}
				fmt.Printf("%s (ID: %d): レベル%d, %dポイント\n", name, m.ChampionID, m.ChampionLevel, m.ChampionPoints)
			}

			// --- 平均マッチランク計算 ---
			fmt.Println("\n直近試合の平均マッチランク計算中...")
			fmt.Printf("[開始] %s#%s: 参加者収集 %d件\n", player.GameName, player.TagLine, maxMatches)
			puuidSet := make(map[string]struct{})
			maxMatches = 10 // デフォルト: 10試合分のみ集計
			if ml := os.Getenv("MATCH_LIMIT"); ml != "" {
				if n, err := strconv.Atoi(ml); err == nil && n > 0 {
					maxMatches = n
				}
			}
			if len(matchIDs) < maxMatches {
				maxMatches = len(matchIDs)
			}
			// 使うマッチ詳細(2回目: 参加者収集)
			counters.AddPlanned(maxMatches)
			for i := 0; i < maxMatches; i++ {
				matchID := matchIDs[i]
				matchDetailUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", matchID)
				matchDetailReq, err := http.NewRequest("GET", matchDetailUrl, nil)
				if err != nil {
					log.Fatal(err)
				}
				matchDetailReq.Header.Set("X-Riot-Token", apiKey)

				matchDetailResp, err := doRequestWithRetry(matchDetailReq, client, limiter, counters, 3)
				if err != nil {
					log.Fatalf("マッチ詳細APIリクエスト失敗: %v", err)
				}
				if matchDetailResp == nil {
					continue
				}
				defer matchDetailResp.Body.Close()

				if matchDetailResp.StatusCode != 200 {
					log.Printf("マッチ詳細APIリクエスト失敗: %s", matchDetailResp.Status)
					continue
				}

				var matchDetail struct {
					Info struct {
						Participants []struct {
							PUUID string `json:"puuid"`
						} `json:"participants"`
					} `json:"info"`
				}
				if err := json.NewDecoder(matchDetailResp.Body).Decode(&matchDetail); err != nil {
					log.Printf("マッチ詳細デコード失敗: %v", err)
					continue
				}
				for _, p := range matchDetail.Info.Participants {
					puuidSet[p.PUUID] = struct{}{}
				}
				// API制限対策（RiotLimiterで吸収）
			}

			// 全PUUIDのランクを取得
			var totalScore, count int
			puuidList := make([]string, 0, len(puuidSet))
			for puuid := range puuidSet {
				puuidList = append(puuidList, puuid)
			}
			fmt.Printf("[開始] %s#%s: 参加者ランク取得 %d人\n", player.GameName, player.TagLine, len(puuidList))
			// ここで参加者ランク問い合わせの総数が確定
			counters.AddPlanned(len(puuidList))
			for _, puuid := range puuidList {
				rankUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/by-puuid/%s", puuid)
				rankReq, err := http.NewRequest("GET", rankUrl, nil)
				if err != nil {
					log.Printf("ランクリクエスト作成失敗: %v", err)
					continue
				}
				rankReq.Header.Set("X-Riot-Token", apiKey)

				rankResp, err := doRequestWithRetry(rankReq, client, limiter, counters, 3)
				if err != nil {
					log.Printf("ランクAPIリクエスト失敗: %v", err)
					continue
				}
				if rankResp == nil {
					continue
				}
				defer rankResp.Body.Close()

				if rankResp.StatusCode != 200 {
					log.Printf("ランクAPIリクエスト失敗: %s", rankResp.Status)
					continue
				}

				var rankData []struct {
					QueueType    string `json:"queueType"`
					Tier         string `json:"tier"`
					Rank         string `json:"rank"`
					LeaguePoints int    `json:"leaguePoints"`
				}
				if err := json.NewDecoder(rankResp.Body).Decode(&rankData); err != nil {
					log.Printf("ランクデコード失敗: %v", err)
					continue
				}
				for _, entry := range rankData {
					if entry.QueueType == "RANKED_SOLO_5x5" {
						score := rankScore(entry.Tier, entry.Rank, entry.LeaguePoints)
						totalScore += score
						count++
						break
					}
				}
				// 進捗表示はメインgoroutineで実施
			}
			if count > 0 {
				avgScore := totalScore / count
				tier, rank, lp := scoreToRank(avgScore)
				fmt.Printf("\n直近10試合の平均マッチランク: %s %s %dLP（%d人分）\n", tier, rank, lp, count)
			} else {
				fmt.Println("\n平均マッチランク: データなし")
			}

			fmt.Printf("\n直近10試合のランク戦回数: %d回\n", rankedCount)
			if rankedCount > 0 {
				fmt.Printf("勝利数: %d回\n勝率: %.1f%%\n", rankedWin, float64(rankedWin)*100/float64(rankedCount))
			} else {
				fmt.Println("勝利数: 0回\n勝率: 0.0%")
			}

			// --- スキルスコア算出 ---
			// 現在のランクスコア
			currentRankScore := 0
			for _, entry := range rankData {
				if entry.QueueType == "RANKED_SOLO_5x5" {
					currentRankScore = rankScore(entry.Tier, entry.Rank, entry.LeaguePoints)
					break
				}
			}
			// 平均マッチランクスコア
			avgRankScore := 0
			if count > 0 {
				avgRankScore = totalScore / count
			}
			// 上位3体のマスタリーポイント合計
			topMastery := 0
			if len(masteries) > 0 {
				sort.Slice(masteries, func(i, j int) bool {
					return masteries[i].ChampionPoints > masteries[j].ChampionPoints
				})
				for i := 0; i < 3 && i < len(masteries); i++ {
					topMastery += masteries[i].ChampionPoints
				}
			}
			// 仮のスキルスコア計算（重み付けは調整可）
			skillScore := currentRankScore*2 + avgRankScore + topMastery/1000

			// --- 得意レーン・チャンピオン抽出 ---
			// レーン
			mainLanes := []string{}
			subLanes := []string{}
			{
				var laneStats []laneStat
				for lane, cnt := range laneCount {
					laneStats = append(laneStats, laneStat{Lane: lane, Count: cnt})
				}
				sort.Slice(laneStats, func(i, j int) bool {
					return laneStats[i].Count > laneStats[j].Count
				})
				for i := 0; i < 2 && i < len(laneStats); i++ {
					mainLanes = append(mainLanes, laneStats[i].Lane)
				}
				for i := 2; i < 4 && i < len(laneStats); i++ {
					subLanes = append(subLanes, laneStats[i].Lane)
				}
			}
			// チャンピオン（マスタリー上位3体＋試合使用上位3体の合成、重複除外、最大6体）
			mainChamps := []string{}
			{
				champSet := make(map[string]struct{})
				// マスタリー上位3体
				if len(masteries) > 0 {
					sort.Slice(masteries, func(i, j int) bool {
						return masteries[i].ChampionPoints > masteries[j].ChampionPoints
					})
					for i := 0; i < 3 && i < len(masteries); i++ {
						name := championIDToName[masteries[i].ChampionID]
						if name == "" {
							name = "不明"
						}
						if _, ok := champSet[name]; !ok && name != "不明" {
							mainChamps = append(mainChamps, name)
							champSet[name] = struct{}{}
						}
						if len(mainChamps) >= 6 {
							break
						}
					}
				}
				// 試合使用上位3体
				if len(mainChamps) < 6 {
					var champStats []champStat
					for id, cnt := range championCount {
						champStats = append(champStats, champStat{ID: id, Count: cnt})
					}
					sort.Slice(champStats, func(i, j int) bool {
						return champStats[i].Count > champStats[j].Count
					})
					for i := 0; i < 3 && i < len(champStats); i++ {
						name := championIDToName[champStats[i].ID]
						if name == "" {
							name = "不明"
						}
						if _, ok := champSet[name]; !ok && name != "不明" {
							mainChamps = append(mainChamps, name)
							champSet[name] = struct{}{}
						}
						if len(mainChamps) >= 6 {
							break
						}
					}
				}
			}

			// --- レーンごとのサブチャンピオン抽出 ---
			fmt.Printf("[開始] %s#%s: レーン別チャンピオン集計 %d件\n", player.GameName, player.TagLine, maxMatches)
			// レーンごとにそのレーンで使ったチャンピオン回数を集計
			laneChampCount := make(map[string]map[int]int) // lane -> champId -> count
			// 使うマッチ詳細(3回目: レーン別チャンプ集計)
			counters.AddPlanned(maxMatches)
			for i := 0; i < maxMatches; i++ {
				matchID := matchIDs[i]
				matchDetailUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", matchID)
				matchDetailReq, err := http.NewRequest("GET", matchDetailUrl, nil)
				if err != nil {
					continue
				}
				matchDetailReq.Header.Set("X-Riot-Token", apiKey)
				matchDetailResp, err := doRequestWithRetry(matchDetailReq, client, limiter, counters, 3)
				if err != nil {
					log.Printf("レーンチャンピオンリクエスト失敗: %v", err)
					continue
				}
				if matchDetailResp == nil {
					continue
				}
				defer matchDetailResp.Body.Close()
				if matchDetailResp.StatusCode != 200 {
					continue
				}
				var matchDetail struct {
					Info struct {
						QueueID      int `json:"queueId"`
						Participants []struct {
							PUUID        string `json:"puuid"`
							ChampionID   int    `json:"championId"`
							TeamPosition string `json:"teamPosition"`
						} `json:"participants"`
					} `json:"info"`
				}
				if err := json.NewDecoder(matchDetailResp.Body).Decode(&matchDetail); err != nil {
					continue
				}
				// アリーナ・クイックプレイ・ARAMは無視
				if matchDetail.Info.QueueID == 1700 || matchDetail.Info.QueueID == 490 || matchDetail.Info.QueueID == 450 {
					continue
				}
				if matchDetail.Info.QueueID != 400 && matchDetail.Info.QueueID != 430 && matchDetail.Info.QueueID != 420 {
					continue
				}
				for _, p := range matchDetail.Info.Participants {
					if p.PUUID == account.PUUID {
						lane := p.TeamPosition
						if lane == "" {
							lane = "UNKNOWN"
						}
						if laneChampCount[lane] == nil {
							laneChampCount[lane] = make(map[int]int)
						}
						laneChampCount[lane][p.ChampionID]++
					}
				}
			}
			// --- レーンごとのサブチャンピオンリスト作成関数 ---
			getLaneChampions := func(lane string) []string {
				champSet := make(map[string]struct{})
				result := []string{}
				// 1. そのレーンでの試合使用上位
				var champStats []champStat
				for id, cnt := range laneChampCount[lane] {
					champStats = append(champStats, champStat{ID: id, Count: cnt})
				}
				sort.Slice(champStats, func(i, j int) bool {
					return champStats[i].Count > champStats[j].Count
				})
				for i := 0; i < 3 && i < len(champStats); i++ {
					name := championIDToName[champStats[i].ID]
					if name == "" {
						name = "不明"
					}
					if _, ok := champSet[name]; !ok && name != "不明" {
						result = append(result, name)
						champSet[name] = struct{}{}
					}
					if len(result) >= 3 {
						break
					}
				}
				// 2. マスタリー上位
				if len(result) < 3 {
					sort.Slice(masteries, func(i, j int) bool {
						return masteries[i].ChampionPoints > masteries[j].ChampionPoints
					})
					for i := 0; i < len(masteries) && len(result) < 3; i++ {
						name := championIDToName[masteries[i].ChampionID]
						if name == "" {
							name = "不明"
						}
						if _, ok := champSet[name]; !ok && name != "不明" {
							result = append(result, name)
							champSet[name] = struct{}{}
						}
					}
				}
				return result
			}
			// main_lanes, main_sublanesごとにサブチャンピオンリストを作成
			mainLaneChamps := map[string][]string{}
			for _, lane := range mainLanes {
				mainLaneChamps[lane] = getLaneChampions(lane)
			}
			subLaneChamps := map[string][]string{}
			for _, lane := range subLanes {
				subLaneChamps[lane] = getLaneChampions(lane)
			}

			// --- AI用データ整形 ---
			playerData := map[string]interface{}{
				"name":                 fmt.Sprintf("%s#%s", player.GameName, player.TagLine),
				"skill_score":          skillScore,
				"current_rank_score":   currentRankScore,
				"avg_match_rank_score": avgRankScore,
				"main_lanes":           mainLanes,
				"main_sublanes":        subLanes,
				"main_lane_champions":  mainLaneChamps,
				"sublane_champions":    subLaneChamps,
				"main_champions":       mainChamps,
				"mastery_top3":         topMastery,
			}
			allPlayerData = append(allPlayerData, playerData)
			fmt.Printf("[完了] %s#%s: 解析完了\n", player.GameName, player.TagLine)
		}
		close(done)
	}()

	// メインgoroutineで定期的に進捗/ETAを表示
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			counters.PrintEstimate("")
		case <-done:
			counters.PrintEstimate("完了")
			goto AFTER_ASYNC
		}
	}

AFTER_ASYNC:

	fmt.Println("\n[開始] チーム分け処理")
	// --- チーム分けロジック ---
	var teamResult map[string]interface{}
	if len(allPlayerData) < 2 {
		fmt.Println("\nチーム分けには2人以上必要です")
		return
	}
	// スキルスコア高い順にソート
	sort.Slice(allPlayerData, func(i, j int) bool {
		return allPlayerData[i]["skill_score"].(int) > allPlayerData[j]["skill_score"].(int)
	})
	teamA := []map[string]interface{}{}
	teamB := []map[string]interface{}{}
	var sumA, sumB int
	for i, p := range allPlayerData {
		if i%2 == 0 {
			teamA = append(teamA, p)
			sumA += p["skill_score"].(int)
		} else {
			teamB = append(teamB, p)
			sumB += p["skill_score"].(int)
		}
	}
	teamResult = map[string]interface{}{
		"teamA": teamA,
		"teamB": teamB,
		"sumA":  sumA,
		"sumB":  sumB,
	}
	fmt.Println("\n=== チーム分け結果 ===")
	fmt.Printf("Aチーム（合計スキル: %d）\n", sumA)
	for _, p := range teamA {
		fmt.Printf("  %s スキル:%d メインレーン:%v\n", p["name"], p["skill_score"], p["main_lanes"])
	}
	fmt.Printf("Bチーム（合計スキル: %d）\n", sumB)
	for _, p := range teamB {
		fmt.Printf("  %s スキル:%d メインレーン:%v\n", p["name"], p["skill_score"], p["main_lanes"])
	}
	// チーム分け結果をJSONファイルに出力
	jsonResult, err := json.MarshalIndent(teamResult, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	err = os.WriteFile("team_result.json", jsonResult, 0644)
	if err != nil {
		log.Fatalf("ファイル出力失敗: %v", err)
	}
	fmt.Println("\nチーム分け結果を team_result.json に出力しました")

    // Discord Webhook 通知は無効化（要求により削除）

	// --- レーン被りなしチーム分けロジック（5人vs5人専用） ---
	if len(allPlayerData) == 10 {
		fmt.Println("\n=== レーン被りなしチーム分け ===")
		// レーンの種類
		// 各プレイヤーの得意レーン
		playerLanes := make([][]string, 10)
		for i, p := range allPlayerData {
			mainLanes, _ := p["main_lanes"].([]string)
			playerLanes[i] = mainLanes
		}
		// 0-9のインデックスで5人選ぶ全組み合わせ
		indices := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
		minDiff := 1 << 30
		var bestA, bestB []int
		var bestAroles, bestBroles []string
		// 全ての5人組み合わせ
		var comb func([]int, int, []int)
		comb = func(arr []int, n int, acc []int) {
			if len(acc) == 5 {
				// accがAチーム、残りがBチーム
				usedA := make(map[string]bool)
				usedB := make(map[string]bool)
				rolesA := make([]string, 5)
				rolesB := make([]string, 5)
				okA, okB := true, true
				// Aチームのレーン割り当て
				for i, idx := range acc {
					found := false
					for _, lane := range playerLanes[idx] {
						if !usedA[lane] {
							usedA[lane] = true
							rolesA[i] = lane
							found = true
							break
						}
					}
					if !found {
						okA = false
						break
					}
				}
				// Bチームのレーン割り当て
				bidx := 0
				for _, idx := range arr {
					inA := false
					for _, a := range acc {
						if idx == a {
							inA = true
							break
						}
					}
					if inA {
						continue
					}
					found := false
					for _, lane := range playerLanes[idx] {
						if !usedB[lane] {
							usedB[lane] = true
							rolesB[bidx] = lane
							found = true
							break
						}
					}
					if !found {
						okB = false
						break
					}
					bidx++
				}
				if okA && okB {
					// スキルスコア合計
					sumA, sumB := 0, 0
					for _, idx := range acc {
						sumA += allPlayerData[idx]["skill_score"].(int)
					}
					for _, idx := range arr {
						inA := false
						for _, a := range acc {
							if idx == a {
								inA = true
								break
							}
						}
						if !inA {
							sumB += allPlayerData[idx]["skill_score"].(int)
						}
					}
					diff := sumA - sumB
					if diff < 0 {
						diff = -diff
					}
					if diff < minDiff {
						minDiff = diff
						bestA = append([]int{}, acc...)
						bestB = []int{}
						for _, idx := range arr {
							inA := false
							for _, a := range acc {
								if idx == a {
									inA = true
									break
								}
							}
							if !inA {
								bestB = append(bestB, idx)
							}
						}
						bestAroles = append([]string{}, rolesA...)
						bestBroles = append([]string{}, rolesB...)
					}
				}
				return
			}
			if n == 0 {
				return
			}
        // 配列が空のときはこれ以上選べないので打ち切り
        if len(arr) == 0 {
            return
        }
        comb(arr[1:], n-1, append(acc, arr[0]))
        comb(arr[1:], n, acc)
		}
		comb(indices, 5, []int{})
		if len(bestA) == 5 && len(bestB) == 5 {
			fmt.Printf("Aチーム（合計スキル: %d）\n", func() int {
				s := 0
				for _, i := range bestA {
					s += allPlayerData[i]["skill_score"].(int)
				}
				return s
			}())
			for i, idx := range bestA {
				fmt.Printf("  %s スキル:%d レーン:%s\n", allPlayerData[idx]["name"], allPlayerData[idx]["skill_score"], bestAroles[i])
			}
			fmt.Printf("Bチーム（合計スキル: %d）\n", func() int {
				s := 0
				for _, i := range bestB {
					s += allPlayerData[i]["skill_score"].(int)
				}
				return s
			}())
			for i, idx := range bestB {
				fmt.Printf("  %s スキル:%d レーン:%s\n", allPlayerData[idx]["name"], allPlayerData[idx]["skill_score"], bestBroles[i])
			}
			return
		}
		fmt.Println("レーン被りなしで分けられる組み合わせがありません")
		return
	}
}
