package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

type leagueEntry struct {
	SummonerID string `json:"summonerId"`
}

type leagueList struct {
	Entries []leagueEntry `json:"entries"`
}

type summonerRes struct {
	PUUID string `json:"puuid"`
}

func getEntries(tier, division, apiKey string) ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	var url string
	if division == "" { // challenger/master/gm
		url = fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/%sleagues/by-queue/RANKED_SOLO_5x5", strings.ToLower(tier))
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("X-Riot-Token", apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var data leagueList
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}
		ids := make([]string, len(data.Entries))
		for i, e := range data.Entries {
			ids[i] = e.SummonerID
		}
		return ids, nil
	}
	url = fmt.Sprintf("https://jp1.api.riotgames.com/lol/league/v4/entries/RANKED_SOLO_5x5/%s/%s?page=1", tier, division)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Riot-Token", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data []leagueEntry
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	ids := make([]string, len(data))
	for i, e := range data {
		ids[i] = e.SummonerID
	}
	return ids, nil
}

func toPUUID(summonerID, apiKey string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("https://jp1.api.riotgames.com/lol/summoner/v4/summoners/%s", summonerID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Riot-Token", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("summoner API status %d", resp.StatusCode)
	}
	var s summonerRes
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	return s.PUUID, nil
}

func main() {
	apiKey := os.Getenv("RIOT_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "RIOT_API_KEY must be set")
		os.Exit(1)
	}
	sampleSize := 100
	tiers := [][]string{
		{"CHALLENGER", ""},
		{"GRANDMASTER", ""},
		{"MASTER", ""},
		{"DIAMOND", "I"},
		{"PLATINUM", "I"},
		{"GOLD", "I"},
		{"SILVER", "I"},
		{"BRONZE", "I"},
		{"IRON", "I"},
	}
	rand.Seed(time.Now().UnixNano())
	type result struct {
		Tier     string `json:"tier"`
		Division string `json:"division,omitempty"`
		PUUID    string `json:"puuid"`
	}
	var out []result
	for _, td := range tiers {
		tier, div := td[0], td[1]
		ids, err := getEntries(tier, div, apiKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "getEntries %s %s: %v\n", tier, div, err)
			continue
		}
		if len(ids) == 0 {
			continue
		}
		n := sampleSize
		if len(ids) < n {
			n = len(ids)
		}
		rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		ids = ids[:n]
		for _, sid := range ids {
			puuid, err := toPUUID(sid, apiKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "toPUUID %s: %v\n", sid, err)
				continue
			}
			out = append(out, result{Tier: tier, Division: div, PUUID: puuid})
			time.Sleep(1200 * time.Millisecond)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}
