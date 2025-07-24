package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
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

// 改良版リトライ付きAPIリクエスト
func doRequestWithRetry(req *http.Request, client *http.Client, maxRetry int) (*http.Response, error) {
	wait := 1 * time.Second
	// SKIPフラグ取得
	skipOnLimit := false
	if os.Getenv("SKIP") == "true" {
		skipOnLimit = true
	}
	var lastStatus int
	for i := 0; i < maxRetry; i++ {
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			return resp, nil
		}
		if resp != nil {
			lastStatus = resp.StatusCode
			if resp.StatusCode == 404 {
				// アンランク等は正常扱い（呼び出し側で判定）
				return resp, nil
			}
			if resp.StatusCode == 429 || (resp.StatusCode >= 500 && resp.StatusCode < 600) {
				// レートリミットや一時的なサーバーエラーは指数バックオフ
				resp.Body.Close()
				if skipOnLimit {
					return nil, nil // SKIP=trueなら無視して次へ
				}
				time.Sleep(wait)
				wait *= 2
				continue
			}
			resp.Body.Close()
		}
		time.Sleep(wait)
	}
	if skipOnLimit {
		return nil, nil // SKIP=trueならリトライ上限でも無視
	}
	return nil, fmt.Errorf("APIリクエスト失敗（リトライ上限, status=%d）", lastStatus)
}

func main() {
	godotenv.Load()
	apiKey := os.Getenv("RIOT_API_KEY")
	if apiKey == "" {
		log.Fatal("RIOT_API_KEYが設定されていません")
	}

	// 複数プレイヤー対応: プレイヤー名リスト
	players := []struct {
		GameName string
		TagLine  string
	}{
		{"ちょぼ", "chobo"},
		// {"exampleuser", "JP1"}, // 追加したい場合ここに
	}

	var allPlayerData []map[string]interface{} // AI用データ格納

	for _, player := range players {
		fmt.Printf("\n==== %s#%s のデータ取得開始 ====\n", player.GameName, player.TagLine)
		gameName := player.GameName // ゲーム名
		tagLine := player.TagLine   // タグライン

		url := fmt.Sprintf("https://asia.api.riotgames.com/riot/account/v1/accounts/by-riot-id/%s/%s", gameName, tagLine)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			log.Fatal(err)
		}
		req.Header.Set("X-Riot-Token", apiKey)

		client := &http.Client{}
		resp, err := doRequestWithRetry(req, client, 3)
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
		matchListUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/by-puuid/%s/ids?start=0&count=100", account.PUUID)
		matchReq, err := http.NewRequest("GET", matchListUrl, nil)
		if err != nil {
			log.Fatal(err)
		}
		matchReq.Header.Set("X-Riot-Token", apiKey)

		matchResp, err := doRequestWithRetry(matchReq, client, 3)
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
		maxMatches := 10                  // 10試合分集計
		if len(matchIDs) < maxMatches {
			maxMatches = len(matchIDs)
		}
		// ランク戦回数・勝利数
		rankedCount := 0
		rankedWin := 0
		for i := 0; i < maxMatches; i++ {
			matchID := matchIDs[i]
			matchDetailUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", matchID)
			matchDetailReq, err := http.NewRequest("GET", matchDetailUrl, nil)
			if err != nil {
				log.Fatal(err)
			}
			matchDetailReq.Header.Set("X-Riot-Token", apiKey)

			matchDetailResp, err := doRequestWithRetry(matchDetailReq, client, 3)
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
			// API制限対策
			time.Sleep(150 * time.Millisecond)
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
		rankUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/by-puuid/%s", account.PUUID)
		rankReq, err := http.NewRequest("GET", rankUrl, nil)
		if err != nil {
			log.Fatal(err)
		}
		rankReq.Header.Set("X-Riot-Token", apiKey)

		rankResp, err := doRequestWithRetry(rankReq, client, 3)
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
		masteryUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/champion-mastery/v4/champion-masteries/by-puuid/%s", account.PUUID)
		masteryReq, err := http.NewRequest("GET", masteryUrl, nil)
		if err != nil {
			log.Fatal(err)
		}
		masteryReq.Header.Set("X-Riot-Token", apiKey)

		masteryResp, err := doRequestWithRetry(masteryReq, client, 3)
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
		fmt.Println("\n直近10試合の平均マッチランク計算中...")
		puuidSet := make(map[string]struct{})
		maxMatches = 10 // 10試合分のみ集計
		if len(matchIDs) < maxMatches {
			maxMatches = len(matchIDs)
		}
		for i := 0; i < maxMatches; i++ {
			matchID := matchIDs[i]
			matchDetailUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", matchID)
			matchDetailReq, err := http.NewRequest("GET", matchDetailUrl, nil)
			if err != nil {
				log.Fatal(err)
			}
			matchDetailReq.Header.Set("X-Riot-Token", apiKey)

			matchDetailResp, err := doRequestWithRetry(matchDetailReq, client, 3)
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
			// API制限対策
			time.Sleep(150 * time.Millisecond)
		}

		// 全PUUIDのランクを取得
		var totalScore, count int
		puuidList := make([]string, 0, len(puuidSet))
		for puuid := range puuidSet {
			puuidList = append(puuidList, puuid)
		}
		startTime := time.Now()
		for i, puuid := range puuidList {
			rankUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/by-puuid/%s", puuid)
			rankReq, err := http.NewRequest("GET", rankUrl, nil)
			if err != nil {
				log.Printf("ランクリクエスト作成失敗: %v", err)
				continue
			}
			rankReq.Header.Set("X-Riot-Token", apiKey)

			rankResp, err := doRequestWithRetry(rankReq, client, 3)
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
			// 進捗・残り時間表示
			if (i+1)%10 == 0 || i == len(puuidList)-1 {
				elapsed := time.Since(startTime)
				remain := time.Duration(len(puuidList)-i-1) * elapsed / time.Duration(i+1)
				fmt.Printf("[%d/%d] 残り予想: %.1f分\n", i+1, len(puuidList), remain.Minutes())
			}
			// API制限対策
			time.Sleep(150 * time.Millisecond)
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
		// レーンごとにそのレーンで使ったチャンピオン回数を集計
		laneChampCount := make(map[string]map[int]int) // lane -> champId -> count
		for i := 0; i < maxMatches; i++ {
			matchID := matchIDs[i]
			matchDetailUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", matchID)
			matchDetailReq, err := http.NewRequest("GET", matchDetailUrl, nil)
			if err != nil {
				continue
			}
			matchDetailReq.Header.Set("X-Riot-Token", apiKey)
			matchDetailResp, err := doRequestWithRetry(matchDetailReq, client, 3)
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
	}

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

	// --- Discord Webhook通知 ---
	webhookURL := ""
	// チーム分け結果をテキストで整形
	msg := "【チーム分け結果】\n"
	msg += fmt.Sprintf("Aチーム（合計スキル: %d）\n", sumA)
	for _, p := range teamA {
		msg += fmt.Sprintf("  %s スキル:%d メインレーン:%v\n", p["name"], p["skill_score"], p["main_lanes"])
	}
	msg += fmt.Sprintf("Bチーム（合計スキル: %d）\n", sumB)
	for _, p := range teamB {
		msg += fmt.Sprintf("  %s スキル:%d メインレーン:%v\n", p["name"], p["skill_score"], p["main_lanes"])
	}
	// Discord Webhookの形式に整形
	payload := map[string]string{"content": msg}
	payloadBytes, _ := json.Marshal(payload)
	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Printf("Discord Webhook送信失敗: %v", err)
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != 204 && resp.StatusCode != 200 {
			log.Printf("Discord Webhook送信失敗: %s", resp.Status)
		}
	}

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
