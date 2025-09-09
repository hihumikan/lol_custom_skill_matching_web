package main

import (
	"fmt"
	"math"
	"sort"
)

// PlayerFeatures holds aggregated statistics for a player.
type PlayerFeatures struct {
	Tier              int
	Rank              int
	LP                int
	WinRate           float64
	SummonerLevel     int
	AvgKDA            float64
	CSPerMin          float64
	GoldPerMin        float64
	VisionPerMin      float64
	DamagePerMin      float64
	KillParticipation float64
	TeamDamagePct     float64
	ObjectiveRate     float64
	TakedownsFirst25  float64
	SoloKills         float64
	MasteryScores     [3]float64
	LaneDistribution  [5]float64
}

// Vector flattens the features into a slice for modeling.
func (p PlayerFeatures) Vector() []float64 {
	v := []float64{
		float64(p.Tier),
		float64(p.Rank),
		float64(p.LP),
		p.WinRate,
		float64(p.SummonerLevel),
		p.AvgKDA,
		p.CSPerMin,
		p.GoldPerMin,
		p.VisionPerMin,
		p.DamagePerMin,
		p.KillParticipation,
		p.TeamDamagePct,
		p.ObjectiveRate,
		p.TakedownsFirst25,
		p.SoloKills,
	}
	for _, s := range p.MasteryScores {
		v = append(v, s)
	}
	for _, d := range p.LaneDistribution {
		v = append(v, d)
	}
	return v
}

// SkillModel is a simple linear model trained with gradient descent.
type SkillModel struct {
	Weights []float64
	Bias    float64
}

// TrainLinear fits a linear regression using gradient descent.
func TrainLinear(players []PlayerFeatures, labels []float64) *SkillModel {
	if len(players) == 0 || len(players) != len(labels) {
		return &SkillModel{}
	}
	dim := len(players[0].Vector())
	w := make([]float64, dim)
	b := 0.0
	lr := 0.000001
	epochs := 1000
	n := float64(len(players))
	for i := 0; i < epochs; i++ {
		gradW := make([]float64, dim)
		gradB := 0.0
		for idx, p := range players {
			x := p.Vector()
			y := labels[idx]
			pred := dot(w, x) + b
			diff := pred - y
			for j := 0; j < dim; j++ {
				gradW[j] += diff * x[j]
			}
			gradB += diff
		}
		for j := 0; j < dim; j++ {
			w[j] -= lr * gradW[j] / n
		}
		b -= lr * gradB / n
	}
	return &SkillModel{Weights: w, Bias: b}
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Predict returns the skill score for a player's features.
func (m *SkillModel) Predict(p PlayerFeatures) float64 {
	if len(m.Weights) == 0 {
		return 0
	}
	return dot(m.Weights, p.Vector()) + m.Bias
}

// BalanceTeams splits players into two teams with minimal skill difference.
func BalanceTeams(players []PlayerFeatures, model *SkillModel) ([]PlayerFeatures, []PlayerFeatures) {
	n := len(players)
	bestDiff := math.MaxFloat64
	var bestA, bestB []PlayerFeatures
	if n <= 10 {
		total := 1 << n
		for mask := 0; mask < total; mask++ {
			var teamA, teamB []PlayerFeatures
			sumA, sumB := 0.0, 0.0
			for i, p := range players {
				if mask&(1<<i) != 0 {
					teamA = append(teamA, p)
					sumA += model.Predict(p)
				} else {
					teamB = append(teamB, p)
					sumB += model.Predict(p)
				}
			}
			if len(teamA) != len(teamB) {
				continue
			}
			diff := math.Abs(sumA - sumB)
			if diff < bestDiff {
				bestDiff = diff
				bestA = teamA
				bestB = teamB
			}
		}
		if len(bestA) > 0 {
			return bestA, bestB
		}
	}
	type ps struct {
		p PlayerFeatures
		s float64
	}
	arr := make([]ps, n)
	for i, p := range players {
		arr[i] = ps{p, model.Predict(p)}
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].s > arr[j].s })
	var teamA, teamB []PlayerFeatures
	sumA, sumB := 0.0, 0.0
	for _, a := range arr {
		if sumA <= sumB {
			teamA = append(teamA, a.p)
			sumA += a.s
		} else {
			teamB = append(teamB, a.p)
			sumB += a.s
		}
	}
	return teamA, teamB
}

func main() {
	// Sample players and labels for demonstration purposes
	players := []PlayerFeatures{
		{Tier: 4, Rank: 1, LP: 50, WinRate: 0.55, SummonerLevel: 100, AvgKDA: 3.0, CSPerMin: 6.5, GoldPerMin: 350, VisionPerMin: 1.2, DamagePerMin: 500, KillParticipation: 0.6, TeamDamagePct: 0.25, ObjectiveRate: 0.05, TakedownsFirst25: 5, SoloKills: 1},
		{Tier: 3, Rank: 2, LP: 20, WinRate: 0.52, SummonerLevel: 80, AvgKDA: 2.5, CSPerMin: 5.8, GoldPerMin: 320, VisionPerMin: 0.9, DamagePerMin: 400, KillParticipation: 0.55, TeamDamagePct: 0.20, ObjectiveRate: 0.03, TakedownsFirst25: 4, SoloKills: 0.5},
		{Tier: 2, Rank: 1, LP: 40, WinRate: 0.50, SummonerLevel: 70, AvgKDA: 2.0, CSPerMin: 5.5, GoldPerMin: 310, VisionPerMin: 0.8, DamagePerMin: 380, KillParticipation: 0.50, TeamDamagePct: 0.18, ObjectiveRate: 0.02, TakedownsFirst25: 3, SoloKills: 0.3},
		{Tier: 1, Rank: 4, LP: 10, WinRate: 0.48, SummonerLevel: 60, AvgKDA: 1.8, CSPerMin: 5.0, GoldPerMin: 300, VisionPerMin: 0.7, DamagePerMin: 360, KillParticipation: 0.45, TeamDamagePct: 0.15, ObjectiveRate: 0.01, TakedownsFirst25: 2, SoloKills: 0.2},
	}
	labels := []float64{1500, 1300, 1200, 1000}
	model := TrainLinear(players, labels)
	teamA, teamB := BalanceTeams(players, model)
	fmt.Println("Team A:", teamA)
	fmt.Println("Team B:", teamB)
}
