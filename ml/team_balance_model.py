"""Machine learning model for estimating player skill and balancing teams.

This module extracts a variety of player and match features from the Riot API
such as rank information, aggregated match statistics, challenge metrics and
champion mastery. The collected features are used to train a regression model
that outputs an internal skill score. These scores are then used to build two
teams with minimal difference in total skill.

Rank is treated as only one of many signals; smurf accounts that perform well
in recent matches should still receive high skill scores even if their rank is
low.
"""
from __future__ import annotations

from dataclasses import dataclass, asdict
from typing import Dict, List, Tuple

import numpy as np
import pandas as pd
import requests
from sklearn.ensemble import GradientBoostingRegressor

# -------------------------------
# Data structures
# -------------------------------


LANES = ["TOP", "JUNGLE", "MIDDLE", "BOTTOM", "UTILITY"]


def tier_to_int(tier: str) -> int:
    """Map tier string to numeric order."""
    order = [
        "IRON",
        "BRONZE",
        "SILVER",
        "GOLD",
        "PLATINUM",
        "EMERALD",
        "DIAMOND",
        "MASTER",
        "GRANDMASTER",
        "CHALLENGER",
    ]
    return order.index(tier.upper()) + 1 if tier else 0


def rank_to_int(rank: str) -> int:
    order = ["IV", "III", "II", "I"]
    return order.index(rank) + 1 if rank in order else 0


@dataclass
class PlayerFeatures:
    """Selected features for a player used for skill estimation.

    The structure intentionally captures a wide variety of statistics so that
    the regression model can learn player strength from many angles (damage,
    vision, objectives, etc.).
    """

    tier: int
    rank: int
    lp: int
    win_rate: float
    summoner_level: int
    avg_kda: float
    cs_per_min: float
    gold_per_min: float
    vision_score_per_min: float
    damage_per_min: float
    kill_participation: float
    team_damage_pct: float
    objective_rate: float
    takedowns_first_25: float
    solo_kills: float
    mastery_scores: List[int]
    lane_distribution: List[float]

    def to_vector(self) -> np.ndarray:
        """Flatten features into numeric vector for model."""
        return np.array(
            [
                self.tier,
                self.rank,
                self.lp,
                self.win_rate,
                self.summoner_level,
                self.avg_kda,
                self.cs_per_min,
                self.gold_per_min,
                self.vision_score_per_min,
                self.damage_per_min,
                self.kill_participation,
                self.team_damage_pct,
                self.objective_rate,
                self.takedowns_first_25,
                self.solo_kills,
                *self.mastery_scores,
                *self.lane_distribution,
            ],
            dtype=float,
        )


# -------------------------------
# Feature extraction from Riot API
# -------------------------------


class RiotAPI:
    """Minimal Riot API client for extracting player features."""

    def __init__(self, api_key: str, region: str = "asia") -> None:
        self.api_key = api_key
        self.session = requests.Session()
        self.region = region  # routing region (americas, asia, europe, sea)

    def _get(self, url: str, params: Dict | None = None) -> Dict:
        headers = {"X-Riot-Token": self.api_key}
        resp = self.session.get(url, params=params, headers=headers, timeout=10)
        resp.raise_for_status()
        return resp.json()

    def get_summoner_basic(self, puuid: str) -> Tuple[int, int]:
        """Return summoner level and profile icon for a PUUID."""
        url = (
            f"https://{self.region}.api.riotgames.com/lol/summoner/v4/summoners/by-puuid/{puuid}"
        )
        data = self._get(url)
        return data.get("summonerLevel", 0), data.get("profileIconId", 0)

    def get_rank(self, summoner_id: str) -> Tuple[int, int, int, float]:
        """Return tier, rank, lp, win rate for SoloQ. Fallback to zero if unranked."""
        url = f"https://{self.region}.api.riotgames.com/lol/league/v4/entries/by-summoner/{summoner_id}"
        data = self._get(url)
        for entry in data:
            if entry.get("queueType") == "RANKED_SOLO_5x5":
                wins = entry.get("wins", 0)
                losses = entry.get("losses", 0)
                total = wins + losses
                win_rate = wins / total if total else 0.0
                return (
                    tier_to_int(entry.get("tier")),
                    rank_to_int(entry.get("rank")),
                    entry.get("leaguePoints", 0),
                    win_rate,
                )
        return 0, 0, 0, 0.0

    def get_recent_matches(self, puuid: str, count: int = 20) -> List[str]:
        url = (
            f"https://{self.region}.api.riotgames.com/lol/match/v5/matches/by-puuid/{puuid}/ids"
        )
        return self._get(url, params={"count": count})

    def get_match_stats(self, match_id: str, puuid: str) -> Dict:
        url = f"https://{self.region}.api.riotgames.com/lol/match/v5/matches/{match_id}"
        data = self._get(url)
        for p in data["info"]["participants"]:
            if p["puuid"] == puuid:
                return {
                    "kda": (p["kills"] + p["assists"]) / max(1, p["deaths"]),
                    "cs_per_min": p["totalMinionsKilled"] / (p["timePlayed"] / 60),
                    "gold_per_min": p["challenges"].get(
                        "goldPerMinute", p["goldEarned"] / (p["timePlayed"] / 60)
                    ),
                    "vision_score_per_min": p["challenges"].get("visionScorePerMinute", 0),
                    "damage_per_min": p["challenges"].get("damagePerMinute", 0),
                    "kill_participation": p["challenges"].get("killParticipation", 0),
                    "team_damage_pct": p["challenges"].get("teamDamagePercentage", 0),
                    "takedowns_first_25": p["challenges"].get("takedownsFirst25Minutes", 0),
                    "solo_kills": p["challenges"].get("soloKills", 0),
                    "objectives": p["challenges"].get("teamBaronKills", 0)
                    + p["challenges"].get("teamDragonKills", 0),
                    "lane": p["teamPosition"],
                    "win": p["win"],
                }
        raise ValueError("PUUID not found in match participants")

    def get_mastery_scores(self, puuid: str, top_n: int = 3) -> List[int]:
        url = (
            f"https://{self.region}.api.riotgames.com/lol/champion-mastery/v4/champion-masteries/by-puuid/{puuid}"
        )
        mastery = self._get(url)
        scores = [m["championPoints"] for m in mastery[:top_n]]
        while len(scores) < top_n:
            scores.append(0)
        return scores

    def build_features(self, puuid: str, summoner_id: str) -> PlayerFeatures:
        tier, rank, lp, win_rate = self.get_rank(summoner_id)
        summoner_level, _icon = self.get_summoner_basic(puuid)
        matches = self.get_recent_matches(puuid)
        stats = [self.get_match_stats(mid, puuid) for mid in matches]
        if stats:
            avg_kda = float(np.mean([s["kda"] for s in stats]))
            cs_per_min = float(np.mean([s["cs_per_min"] for s in stats]))
            gold_per_min = float(np.mean([s["gold_per_min"] for s in stats]))
            vision_score_per_min = float(
                np.mean([s["vision_score_per_min"] for s in stats])
            )
            damage_per_min = float(np.mean([s["damage_per_min"] for s in stats]))
            kill_participation = float(
                np.mean([s["kill_participation"] for s in stats])
            )
            team_damage_pct = float(
                np.mean([s["team_damage_pct"] for s in stats])
            )
            objective_rate = float(np.mean([s["objectives"] for s in stats]))
            takedowns_first_25 = float(
                np.mean([s["takedowns_first_25"] for s in stats])
            )
            solo_kills = float(np.mean([s["solo_kills"] for s in stats]))
            lane_counts = {lane: 0 for lane in LANES}
            for s in stats:
                lane_counts[s["lane"]] = lane_counts.get(s["lane"], 0) + 1
            lane_distribution = [lane_counts[l] / len(stats) for l in LANES]
        else:
            (
                avg_kda,
                cs_per_min,
                gold_per_min,
                vision_score_per_min,
                damage_per_min,
                kill_participation,
                team_damage_pct,
                objective_rate,
                takedowns_first_25,
                solo_kills,
            ) = (0.0,) * 10
            lane_distribution = [0.0 for _ in LANES]
        mastery_scores = self.get_mastery_scores(puuid)
        return PlayerFeatures(
            tier=tier,
            rank=rank,
            lp=lp,
            win_rate=win_rate,
            summoner_level=summoner_level,
            avg_kda=avg_kda,
            cs_per_min=cs_per_min,
            gold_per_min=gold_per_min,
            vision_score_per_min=vision_score_per_min,
            damage_per_min=damage_per_min,
            kill_participation=kill_participation,
            team_damage_pct=team_damage_pct,
            objective_rate=objective_rate,
            takedowns_first_25=takedowns_first_25,
            solo_kills=solo_kills,
            mastery_scores=mastery_scores,
            lane_distribution=lane_distribution,
        )


# -------------------------------
# Model training and usage
# -------------------------------


def train_skill_model(features: List[PlayerFeatures], labels: List[float]) -> GradientBoostingRegressor:
    """Train regression model to predict skill scores."""
    X = np.vstack([f.to_vector() for f in features])
    y = np.array(labels, dtype=float)
    model = GradientBoostingRegressor(random_state=0)
    model.fit(X, y)
    return model


def predict_skill(model: GradientBoostingRegressor, feat: PlayerFeatures) -> float:
    return float(model.predict([feat.to_vector()])[0])


def balance_teams(players: Dict[str, float]) -> Tuple[List[str], List[str]]:
    """Assign players into two teams with minimal skill difference."""
    names = list(players.keys())
    skills = np.array([players[n] for n in names])
    best_diff = float("inf")
    best_split: Tuple[List[str], List[str]] | None = None
    n = len(names)
    # iterate over half combinations
    from itertools import combinations

    for r in combinations(range(n), n // 2):
        team1 = np.zeros(n, dtype=bool)
        team1[list(r)] = True
        score1 = skills[team1].sum()
        score2 = skills[~team1].sum()
        diff = abs(score1 - score2)
        if diff < best_diff:
            best_diff = diff
            team_a = [names[i] for i in range(n) if team1[i]]
            team_b = [names[i] for i in range(n) if not team1[i]]
            best_split = (team_a, team_b)
    assert best_split is not None
    return best_split


def features_to_dataframe(features: List[PlayerFeatures]) -> pd.DataFrame:
    return pd.DataFrame([asdict(f) for f in features])


__all__ = [
    "PlayerFeatures",
    "RiotAPI",
    "train_skill_model",
    "predict_skill",
    "balance_teams",
    "features_to_dataframe",
]
