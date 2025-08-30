package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "sort"
    "strconv"
    "strings"
    "time"
    
    "github.com/joho/godotenv"
)

// Minimal types reused from CLI
type Player struct {
    GameName string `json:"gameName"`
    TagLine  string `json:"tagLine"`
}

type analyzeRequest struct {
    Players    []Player `json:"players"`
    MatchLimit int      `json:"matchLimit,omitempty"`
}

// Tier/Rank maps
var tierToInt = map[string]int{
    "IRON": 1, "BRONZE": 2, "SILVER": 3, "GOLD": 4, "PLATINUM": 5,
    "EMERALD": 6, "DIAMOND": 7, "MASTER": 8, "GRANDMASTER": 9, "CHALLENGER": 10,
}
var intToTier = map[int]string{1: "IRON", 2: "BRONZE", 3: "SILVER", 4: "GOLD", 5: "PLATINUM", 6: "EMERALD", 7: "DIAMOND", 8: "MASTER", 9: "GRANDMASTER", 10: "CHALLENGER"}
var rankToInt = map[string]int{"IV": 1, "III": 2, "II": 3, "I": 4}
var intToRank = map[int]string{1: "IV", 2: "III", 3: "II", 4: "I"}

func rankScore(tier, rank string, lp int) int {
    t := tierToInt[tier]
    r := rankToInt[rank]
    return ((t-1)*4+(r-1))*100 + lp
}
func scoreToRank(score int) (string, string, int) {
    tierIdx := score/400 + 1
    rankIdx := (score%400)/100 + 1
    lp := score % 100
    return intToTier[tierIdx], intToRank[rankIdx], lp
}

// Basic rate limiter matching CLI behavior
type RiotLimiter struct {
    secWin []time.Time
    twoMin []time.Time
}
func (r *RiotLimiter) Wait() {
    for {
        now := time.Now()
        cutoff1 := now.Add(-1 * time.Second)
        for len(r.secWin) > 0 && r.secWin[0].Before(cutoff1) {
            r.secWin = r.secWin[1:]
        }
        cutoff2 := now.Add(-120 * time.Second)
        for len(r.twoMin) > 0 && r.twoMin[0].Before(cutoff2) {
            r.twoMin = r.twoMin[1:]
        }
        if len(r.secWin) < 20 && len(r.twoMin) < 100 {
            r.secWin = append(r.secWin, now)
            r.twoMin = append(r.twoMin, now)
            return
        }
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
        time.Sleep(sleepFor)
    }
}

func doRequestWithRetry(req *http.Request, client *http.Client, limiter *RiotLimiter, maxRetry int) (*http.Response, error) {
    skipOnLimit := os.Getenv("SKIP") == "true"
    backoff := 1 * time.Second
    tries := 0
    var lastStatus int
    for {
        limiter.Wait()
        tries++
        resp, err := client.Do(req)
        if err == nil && resp != nil && resp.StatusCode == 200 {
            return resp, nil
        }
        if resp != nil {
            lastStatus = resp.StatusCode
            if resp.StatusCode == 404 {
                return resp, nil
            }
            if resp.StatusCode == 429 {
                ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
                resp.Body.Close()
                var wait time.Duration
                if ra != "" {
                    if v, err := strconv.Atoi(ra); err == nil {
                        wait = time.Duration(v) * time.Second
                    }
                }
                if wait == 0 {
                    wait = 2 * time.Second
                }
                if skipOnLimit {
                    return nil, nil
                }
                time.Sleep(wait)
                continue
            }
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
            resp.Body.Close()
        }
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
    return nil, fmt.Errorf("request failed after retries, status=%d", lastStatus)
}

func analyze(ctx context.Context, apiKey string, players []Player, matchLimit int) (map[string]interface{}, error) {
    if len(players) < 2 {
        return nil, fmt.Errorf("need at least 2 players")
    }
    client := &http.Client{}
    limiter := &RiotLimiter{}

    // champion id -> name map
    championIDToName := map[int]string{}
    {
        req, _ := http.NewRequestWithContext(ctx, "GET", "https://ddragon.leagueoflegends.com/cdn/15.14.1/data/ja_JP/champion.json", nil)
        resp, err := client.Do(req)
        if err == nil && resp != nil && resp.StatusCode == 200 {
            defer resp.Body.Close()
            var champData struct {
                Data map[string]struct {
                    Key  string `json:"key"`
                    Name string `json:"name"`
                } `json:"data"`
            }
            if err := json.NewDecoder(resp.Body).Decode(&champData); err == nil {
                for _, v := range champData.Data {
                    var id int
                    fmt.Sscanf(v.Key, "%d", &id)
                    championIDToName[id] = v.Name
                }
            }
        }
    }

    allPlayerData := make([]map[string]interface{}, 0, len(players))

    for _, player := range players {
        // 1) account by riot-id
        url := fmt.Sprintf("https://asia.api.riotgames.com/riot/account/v1/accounts/by-riot-id/%s/%s", player.GameName, player.TagLine)
        req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
        req.Header.Set("X-Riot-Token", apiKey)
        resp, err := doRequestWithRetry(req, client, limiter, 3)
        if err != nil || resp == nil || (resp.StatusCode != 200 && resp.StatusCode != 404) {
            if resp != nil { resp.Body.Close() }
            return nil, fmt.Errorf("account lookup failed for %s#%s", player.GameName, player.TagLine)
        }
        var account struct{
            PUUID    string `json:"puuid"`
            GameName string `json:"gameName"`
            TagLine  string `json:"tagLine"`
        }
        if resp.StatusCode == 200 {
            if err := json.NewDecoder(resp.Body).Decode(&account); err != nil { resp.Body.Close(); return nil, err }
            resp.Body.Close()
        } else {
            // 404: skip
            resp.Body.Close()
            continue
        }

        // 2) match list by puuid
        matchListUrl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/by-puuid/%s/ids?start=0&count=100", account.PUUID)
        mreq, _ := http.NewRequestWithContext(ctx, "GET", matchListUrl, nil)
        mreq.Header.Set("X-Riot-Token", apiKey)
        mresp, err := doRequestWithRetry(mreq, client, limiter, 3)
        if err != nil || mresp == nil || mresp.StatusCode != 200 {
            if mresp != nil { mresp.Body.Close() }
            return nil, fmt.Errorf("failed to get matches for %s", account.PUUID)
        }
        var matchIDs []string
        if err := json.NewDecoder(mresp.Body).Decode(&matchIDs); err != nil { mresp.Body.Close(); return nil, err }
        mresp.Body.Close()
        if matchLimit <= 0 || matchLimit > len(matchIDs) { matchLimit = len(matchIDs) }

        championCount := map[int]int{}
        laneCount := map[string]int{}
        laneChampCount := make(map[string]map[int]int) // lane -> champId -> count
        rankedCount := 0
        rankedWin := 0
        puuidSet := make(map[string]struct{})

        // 3) details pass 1: count champs and lanes, track ranked matches
        for i := 0; i < matchLimit; i++ {
            mid := matchIDs[i]
            durl := fmt.Sprintf("https://asia.api.riotgames.com/lol/match/v5/matches/%s", mid)
            dreq, _ := http.NewRequestWithContext(ctx, "GET", durl, nil)
            dreq.Header.Set("X-Riot-Token", apiKey)
            dresp, err := doRequestWithRetry(dreq, client, limiter, 3)
            if err != nil || dresp == nil || dresp.StatusCode != 200 { if dresp != nil { dresp.Body.Close() }; continue }
            var detail struct { Info struct { QueueID int `json:"queueId"`; Participants []struct{ PUUID string `json:"puuid"`; ChampionID int `json:"championId"`; TeamPosition string `json:"teamPosition"`; Win bool `json:"win"` } `json:"participants"` } `json:"info"` }
            if err := json.NewDecoder(dresp.Body).Decode(&detail); err != nil { dresp.Body.Close(); continue }
            dresp.Body.Close()
            if detail.Info.QueueID == 1700 || detail.Info.QueueID == 490 || detail.Info.QueueID == 450 { continue }
            if detail.Info.QueueID != 400 && detail.Info.QueueID != 430 && detail.Info.QueueID != 420 { continue }
            for _, p := range detail.Info.Participants {
                puuidSet[p.PUUID] = struct{}{}
                if p.PUUID == account.PUUID {
                    championCount[p.ChampionID]++
                    lane := p.TeamPosition
                    if lane == "" { lane = "UNKNOWN" }
                    laneCount[lane]++
                    if laneChampCount[lane] == nil { laneChampCount[lane] = make(map[int]int) }
                    laneChampCount[lane][p.ChampionID]++
                    if detail.Info.QueueID == 420 { rankedCount++; if p.Win { rankedWin++ } }
                }
            }
        }

        // rank by puuid (current)
        rankUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/by-puuid/%s", account.PUUID)
        rreq, _ := http.NewRequestWithContext(ctx, "GET", rankUrl, nil)
        rreq.Header.Set("X-Riot-Token", apiKey)
        rresp, err := doRequestWithRetry(rreq, client, limiter, 3)
        var currentRankScore int
        if err == nil && rresp != nil && rresp.StatusCode == 200 {
            var ranks []struct{ QueueType, Tier, Rank string; LeaguePoints int }
            if err := json.NewDecoder(rresp.Body).Decode(&ranks); err == nil {
                for _, e := range ranks { if e.QueueType == "RANKED_SOLO_5x5" { currentRankScore = rankScore(e.Tier, e.Rank, e.LeaguePoints); break } }
            }
            rresp.Body.Close()
        } else if rresp != nil { rresp.Body.Close() }

        // mastery by puuid (top3 sum)
        masteryUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/champion-mastery/v4/champion-masteries/by-puuid/%s", account.PUUID)
        m2req, _ := http.NewRequestWithContext(ctx, "GET", masteryUrl, nil)
        m2req.Header.Set("X-Riot-Token", apiKey)
        m2resp, err := doRequestWithRetry(m2req, client, limiter, 3)
        topMastery := 0
        var masteries []struct{ ChampionID, ChampionLevel, ChampionPoints int }
        if err == nil && m2resp != nil && m2resp.StatusCode == 200 {
            if err := json.NewDecoder(m2resp.Body).Decode(&masteries); err == nil {
                sort.Slice(masteries, func(i, j int) bool { return masteries[i].ChampionPoints > masteries[j].ChampionPoints })
                for i := 0; i < 3 && i < len(masteries); i++ { topMastery += masteries[i].ChampionPoints }
            }
            m2resp.Body.Close()
        } else if m2resp != nil { m2resp.Body.Close() }

        // lanes
        var laneStats []struct{ Lane string; Count int }
        for k, v := range laneCount { laneStats = append(laneStats, struct{ Lane string; Count int }{k, v}) }
        sort.Slice(laneStats, func(i, j int) bool { return laneStats[i].Count > laneStats[j].Count })
        mainLanes := []string{}
        subLanes := []string{}
        for i := 0; i < 2 && i < len(laneStats); i++ { mainLanes = append(mainLanes, laneStats[i].Lane) }
        for i := 2; i < 4 && i < len(laneStats); i++ { subLanes = append(subLanes, laneStats[i].Lane) }

        // main champs (mix of mastery top and match usage top, max 6)
        mainChamps := []string{}
        champSet := map[string]struct{}{}
        // top3 mastery names
        {
            masteryUrl2 := fmt.Sprintf("https://jp1.api.riotgames.com/lol/champion-mastery/v4/champion-masteries/by-puuid/%s", account.PUUID)
            req2, _ := http.NewRequestWithContext(ctx, "GET", masteryUrl2, nil)
            req2.Header.Set("X-Riot-Token", apiKey)
            resp2, err := doRequestWithRetry(req2, client, limiter, 3)
            if err == nil && resp2 != nil && resp2.StatusCode == 200 {
                var masteries []struct{ ChampionID, ChampionPoints int }
                if err := json.NewDecoder(resp2.Body).Decode(&masteries); err == nil {
                    sort.Slice(masteries, func(i, j int) bool { return masteries[i].ChampionPoints > masteries[j].ChampionPoints })
                    for i := 0; i < len(masteries) && len(mainChamps) < 3; i++ {
                        name := championIDToName[masteries[i].ChampionID]
                        if name != "" { if _, ok := champSet[name]; !ok { mainChamps = append(mainChamps, name); champSet[name] = struct{}{} } }
                    }
                }
                resp2.Body.Close()
            } else if resp2 != nil { resp2.Body.Close() }
        }
        if len(mainChamps) < 6 {
            // usage top
            type cs struct{ ID, Count int }
            arr := []cs{}
            for id, cnt := range championCount { arr = append(arr, cs{id, cnt}) }
            sort.Slice(arr, func(i, j int) bool { return arr[i].Count > arr[j].Count })
            for i := 0; i < len(arr) && len(mainChamps) < 6; i++ {
                name := championIDToName[arr[i].ID]
                if name != "" { if _, ok := champSet[name]; !ok { mainChamps = append(mainChamps, name); champSet[name] = struct{}{} } }
            }
        }

        // Average match rank score across participants of recent matches
        totalScore, count := 0, 0
        for puuid := range puuidSet {
            rankUrl := fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/by-puuid/%s", puuid)
            rreq, _ := http.NewRequestWithContext(ctx, "GET", rankUrl, nil)
            rreq.Header.Set("X-Riot-Token", apiKey)
            rresp, err := doRequestWithRetry(rreq, client, limiter, 3)
            if err != nil || rresp == nil || rresp.StatusCode != 200 { if rresp != nil { rresp.Body.Close() }; continue }
            var rdata []struct{ QueueType, Tier, Rank string; LeaguePoints int }
            if err := json.NewDecoder(rresp.Body).Decode(&rdata); err == nil {
                for _, e := range rdata {
                    if e.QueueType == "RANKED_SOLO_5x5" {
                        totalScore += rankScore(e.Tier, e.Rank, e.LeaguePoints)
                        count++
                        break
                    }
                }
            }
            rresp.Body.Close()
        }
        avgRankScore := 0
        if count > 0 { avgRankScore = totalScore / count }

        skillScore := currentRankScore*2 + avgRankScore + topMastery/1000
        // lane-specific sub champions (top by usage, then mastery)
        getLaneChampions := func(lane string) []string {
            champSet := make(map[string]struct{})
            result := []string{}
            type cs struct{ ID, Count int }
            arr := []cs{}
            for id, c := range laneChampCount[lane] { arr = append(arr, cs{id, c}) }
            sort.Slice(arr, func(i, j int) bool { return arr[i].Count > arr[j].Count })
            for i := 0; i < len(arr) && len(result) < 3; i++ {
                if name := championIDToName[arr[i].ID]; name != "" { if _, ok := champSet[name]; !ok { result = append(result, name); champSet[name] = struct{}{} } }
            }
            if len(result) < 3 && len(masteries) > 0 {
                sort.Slice(masteries, func(i, j int) bool { return masteries[i].ChampionPoints > masteries[j].ChampionPoints })
                for i := 0; i < len(masteries) && len(result) < 3; i++ {
                    if name := championIDToName[masteries[i].ChampionID]; name != "" { if _, ok := champSet[name]; !ok { result = append(result, name); champSet[name] = struct{}{} } }
                }
            }
            return result
        }
        mainLaneChamps := map[string][]string{}
        for _, lane := range mainLanes { mainLaneChamps[lane] = getLaneChampions(lane) }
        subLaneChamps := map[string][]string{}
        for _, lane := range subLanes { subLaneChamps[lane] = getLaneChampions(lane) }

        playerData := map[string]interface{}{
            "name":                  fmt.Sprintf("%s#%s", player.GameName, player.TagLine),
            "skill_score":           skillScore,
            "current_rank_score":    currentRankScore,
            "avg_match_rank_score":  avgRankScore,
            "main_lanes":            mainLanes,
            "main_sublanes":         subLanes,
            "main_champions":        mainChamps,
            "main_lane_champions":   mainLaneChamps,
            "sublane_champions":     subLaneChamps,
            "mastery_top3":          topMastery,
            "ranked_recent_count":   rankedCount,
            "ranked_recent_wins":    rankedWin,
        }
        allPlayerData = append(allPlayerData, playerData)
    }

    // team split by alternating after sorting by skill
    sort.Slice(allPlayerData, func(i, j int) bool { return allPlayerData[i]["skill_score"].(int) > allPlayerData[j]["skill_score"].(int) })
    teamA := []map[string]interface{}{}
    teamB := []map[string]interface{}{}
    sumA, sumB := 0, 0
    for i, p := range allPlayerData {
        if i%2 == 0 { teamA = append(teamA, p); sumA += p["skill_score"].(int) } else { teamB = append(teamB, p); sumB += p["skill_score"].(int) }
    }
    result := map[string]interface{}{"teamA": teamA, "teamB": teamB, "sumA": sumA, "sumB": sumB}

    // lane-unique team split for 10 players (optional parity with CLI)
    if len(allPlayerData) == 10 {
        indices := []int{0,1,2,3,4,5,6,7,8,9}
        minDiff := 1<<30
        var bestA, bestB []int
        var bestAroles, bestBroles []string
        playerLanes := make([][]string, 10)
        for i, p := range allPlayerData { if lanes, ok := p["main_lanes"].([]string); ok { playerLanes[i] = lanes } }
        var comb func([]int, int, []int)
        comb = func(arr []int, n int, acc []int) {
            if len(acc) == 5 {
                usedA, usedB := map[string]bool{}, map[string]bool{}
                rolesA, rolesB := make([]string, 5), make([]string, 5)
                okA, okB := true, true
                for i, idx := range acc {
                    found := false
                    for _, lane := range playerLanes[idx] { if !usedA[lane] { usedA[lane] = true; rolesA[i] = lane; found = true; break } }
                    if !found { okA = false; break }
                }
                bidx := 0
                if okA {
                    for _, idx := range arr {
                        inA := false
                        for _, a := range acc { if idx == a { inA = true; break } }
                        if inA { continue }
                        found := false
                        for _, lane := range playerLanes[idx] { if !usedB[lane] { usedB[lane] = true; rolesB[bidx] = lane; found = true; break } }
                        if !found { okB = false; break }
                        bidx++
                    }
                }
                if okA && okB {
                    sA, sB := 0, 0
                    for _, idx := range acc { sA += allPlayerData[idx]["skill_score"].(int) }
                    for _, idx := range arr {
                        inA := false
                        for _, a := range acc { if idx == a { inA = true; break } }
                        if !inA { sB += allPlayerData[idx]["skill_score"].(int) }
                    }
                    d := sA - sB; if d < 0 { d = -d }
                    if d < minDiff { minDiff = d; bestA = append([]int{}, acc...); bestB = []int{}; for _, idx := range arr { inA := false; for _, a := range acc { if idx == a { inA = true; break } }; if !inA { bestB = append(bestB, idx) } }; bestAroles = append([]string{}, rolesA...); bestBroles = append([]string{}, rolesB...) }
                }
                return
            }
            if n == 0 { return }
            if len(arr) == 0 { return }
            comb(arr[1:], n-1, append(acc, arr[0]))
            comb(arr[1:], n, acc)
        }
        comb(indices, 5, []int{})
        if len(bestA) == 5 && len(bestB) == 5 {
            type entry struct { Name string `json:"name"`; Role string `json:"role"`; Skill int `json:"skill"` }
            outA, outB := []entry{}, []entry{}
            sumRA, sumRB := 0, 0
            for i, idx := range bestA { outA = append(outA, entry{ Name: allPlayerData[idx]["name"].(string), Role: bestAroles[i], Skill: allPlayerData[idx]["skill_score"].(int) }); sumRA += allPlayerData[idx]["skill_score"].(int) }
            for i, idx := range bestB { outB = append(outB, entry{ Name: allPlayerData[idx]["name"].(string), Role: bestBroles[i], Skill: allPlayerData[idx]["skill_score"].(int) }); sumRB += allPlayerData[idx]["skill_score"].(int) }
            result["lane_unique"] = map[string]interface{}{ "teamA": outA, "teamB": outB, "sumA": sumRA, "sumB": sumRB }
        }
    }
    return result, nil
}

func withCORS(h http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
        w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        if r.Method == http.MethodOptions { w.WriteHeader(http.StatusNoContent); return }
        h.ServeHTTP(w, r)
    })
}

// ---- Simple request logging middleware ----
type ctxKey string

const ctxReqID ctxKey = "reqID"

type loggingResponseWriter struct {
    http.ResponseWriter
    status int
    nbytes int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
    lw.status = code
    lw.ResponseWriter.WriteHeader(code)
}
func (lw *loggingResponseWriter) Write(b []byte) (int, error) {
    if lw.status == 0 {
        lw.status = http.StatusOK
    }
    n, err := lw.ResponseWriter.Write(b)
    lw.nbytes += n
    return n, err
}

func reqID() string { return fmt.Sprintf("%x", time.Now().UnixNano()) }

func clientIP(r *http.Request) string {
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        return strings.Split(xff, ",")[0]
    }
    if xr := r.Header.Get("X-Real-IP"); xr != "" {
        return xr
    }
    return r.RemoteAddr
}

func logRequests(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := reqID()
        start := time.Now()
        lw := &loggingResponseWriter{ResponseWriter: w}
        ctx := context.WithValue(r.Context(), ctxReqID, id)
        log.Printf("[req %s] %s %s from %s", id, r.Method, r.URL.Path, clientIP(r))
        next.ServeHTTP(lw, r.WithContext(ctx))
        dur := time.Since(start)
        log.Printf("[req %s] done status=%d bytes=%d dur=%s", id, lw.status, lw.nbytes, dur)
    })
}

func main() {
    // Load env from .env (cwd=backend via Makefile). Fallback to backend/.env when executed from repo root.
    if err := godotenv.Load(); err != nil {
        _ = godotenv.Load("backend/.env")
    }

    // Env
    apiKey := os.Getenv("RIOT_API_KEY")
    if apiKey == "" {
        log.Fatal("RIOT_API_KEY is required for the web API server")
    }
    matchLimit := 10
    if ml := os.Getenv("MATCH_LIMIT"); ml != "" {
        if n, err := strconv.Atoi(ml); err == nil && n > 0 { matchLimit = n }
    }

    // optional: log to file if LOG_FILE is set
    if lf := os.Getenv("LOG_FILE"); lf != "" {
        if f, err := os.OpenFile(lf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
            log.Printf("logging to %s", lf)
            log.SetOutput(f)
        } else {
            log.Printf("failed to open LOG_FILE=%s: %v", lf, err)
        }
    }

    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK); _, _ = w.Write([]byte("ok")) })
    mux.HandleFunc("/analyze", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
        var req analyzeRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil { http.Error(w, "invalid json", http.StatusBadRequest); return }
        // freeze current reqID for logs
        rid, _ := r.Context().Value(ctxReqID).(string)
        if req.MatchLimit > 0 { matchLimit = req.MatchLimit }
        log.Printf("[req %s] analyze start players=%d matchLimit=%d", rid, len(req.Players), matchLimit)
        ctx := r.Context()
        astart := time.Now()
        result, err := analyze(ctx, apiKey, req.Players, matchLimit)
        if err != nil {
            log.Printf("[req %s] analyze error: %v", rid, err)
            http.Error(w, err.Error(), http.StatusBadRequest); return
        }
        // also write result to file for traceability
        resultFile := os.Getenv("RESULT_FILE")
        if resultFile == "" { resultFile = "team_result.json" }
        if b, mErr := json.MarshalIndent(result, "", "  "); mErr == nil {
            if wErr := os.WriteFile(resultFile, b, 0644); wErr != nil {
                log.Printf("[req %s] failed to write result file (%s): %v", rid, resultFile, wErr)
            } else {
                log.Printf("[req %s] wrote result to %s", rid, resultFile)
            }
        } else {
            log.Printf("[req %s] marshal result failed: %v", rid, mErr)
        }
        dur := time.Since(astart)
        // attach simple meta for progress/diagnostics
        if m, ok := result["meta"].(map[string]interface{}); ok {
            m["duration_ms"] = dur.Milliseconds()
            m["players"] = len(req.Players)
            m["match_limit"] = matchLimit
        } else {
            result["meta"] = map[string]interface{}{
                "duration_ms": dur.Milliseconds(),
                "players": len(req.Players),
                "match_limit": matchLimit,
            }
        }
        log.Printf("[req %s] analyze done in %s", rid, dur)
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(result)
    })

    port := os.Getenv("PORT")
    if port == "" { port = "8080" }
    addr := ":" + port
    log.Printf("Web API listening on %s", addr)
    if err := http.ListenAndServe(addr, logRequests(withCORS(mux))); err != nil { log.Fatal(err) }
}
