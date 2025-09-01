# Machine Learning Team Balancer

This folder contains a simple Python implementation that estimates player
skill from Riot API data and proposes balanced teams for custom matches.

## Potential Features
The model can ingest many kinds of information that describe a player's real
strength. Examples include:

1. **Rank & Player info** – summoner level, profile icon, tier/division/LP,
   win rate, champion mastery, lane distribution.
2. **Match metadata** – queueId, game mode, game version, map, match duration
   and team objective counts (dragons, barons, towers).
3. **Participant statistics** – KDA, damage dealt/taken, gold earned,
   minions/CS, vision stats (wards placed/killed, detector wards), items and
   spell casts, role information, first blood/tower flags.
4. **Challenge metrics** – damagePerMinute, killParticipation,
   teamDamagePercentage, visionScorePerMinute, goldPerMinute,
   takedownsFirst25Minutes, soloKills and many more.
5. **Timeline data** – per-frame gold, XP, minions, position coordinates,
   item purchases and other in-game events.
6. **Aggregated features** – recent N-match averages, ping counts
   (assistMe/onMyWay), special achievements such as perfectGame or
   flawlessAces.

Rank is only one weak indicator. Combining it with recent performance and
challenge metrics lets the model rate strong smurf accounts appropriately.

## Usage
1. Install dependencies (pandas, requests, scikit-learn):
   ```bash
   pip install pandas requests scikit-learn
   ```
2. Create a `RiotAPI` instance with an API key.
3. Call `build_features` for each player to create `PlayerFeatures`.
4. Train a model using `train_skill_model` with historical labels (e.g.,
   match win contribution or converted MMR).
5. Predict skills for the current participants and call `balance_teams` to
   obtain two teams with minimal skill difference.

## Training pipeline overview
1. **Data collection** – batch Riot API calls to accumulate large amounts of
   player data.
2. **Preprocessing** – handle missing values, normalize/standardize numeric
   features and one-hot encode categorical ones (e.g. tiers).
3. **Split** – divide into train/validation/test sets (e.g. 70/15/15).
4. **Modeling** – train regression models such as XGBoost or
   GradientBoostingRegressor. Use grid search or Bayesian optimization for
   hyper-parameter tuning.
5. **Evaluation** – measure RMSE, MAE and rank correlation (Spearman).
6. **Model storage** – export models via joblib or ONNX for later inference.

The training data and labels are not included; integrate this module with your
existing data collection pipeline.

This module is intentionally lightweight; extend it with additional features or
alternative models as more data becomes available.
