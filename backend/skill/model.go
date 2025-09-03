package skill

import (
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
