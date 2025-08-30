# lol_custom_skill_matching

LoL カスタム戦の参加メンバーから、各種データ（ランク、直近試合、マスタリーなど）を収集し、バランスの良いチーム分けを提案するツールです。CLI と Web API、React フロントエンドを同梱しています。Docker はローカル用と公開用を分離し、GitHub Actions で公開用イメージのビルド・配布に対応しています。

## 主な機能
- バックエンド（CLI）：`backend/cmd/main.go`
  - `backend/players.json` もしくは `PLAYERS_FILE` で指定した JSON からプレイヤー一覧を読み込み、`team_result.json` を出力。
  - Riot API のレート制限（20 req/s, 100 req/120s）と 429 リトライを考慮。
  - 直近試合からレーンや使用チャンピオンの傾向を集計、マスタリーやランク情報から簡易スキルスコアを算出。

- バックエンド（Web API）：`backend/cmd/app`
  - `POST /analyze` にプレイヤー一覧を渡すと、チーム分け結果（`teamA`/`teamB`/合計スキル）を JSON で返却。
  - `GET /healthz` 健康診断。

- フロントエンド（React + Vite）：`front/`
  - ロビー参加ログ（例：「名前#タグがロビーに参加しました」）を貼り付け、「登録」ボタンで一括追加。
  - 最大 10 人まで登録。登録済みは読み取り専用の行で表示し、個別削除可能。未満時は単一入力行で手動追加可。
  - 「解析」ボタンで Web API にリクエストし、チーム結果を表示。

## 必要要件
- Go 1.24.4+
- Node.js + pnpm もしくは npm（フロント開発）
- Riot API Key（`RIOT_API_KEY`）

`backend/.env` に以下のように設定します（例）:

```
RIOT_API_KEY=YOUR_RIOT_API_KEY
SKIP=false
MATCH_LIMIT=10
```

## クイックスタート（ローカル開発）
1) 依存関係の準備

```
make setup
```

2-a) CLI を動かす（`backend/players.json` を使用）

```
make back-run
# 実行後、backend/team_result.json が生成されます
```

2-b) Web API + フロントで動かす

```
make dev-app
# バックエンドが :8080、フロントが Vite で起動します
```

ブラウザでフロントを開き、ロビー参加ログを貼り付けて「登録」→「解析」。

## CLI（詳細）
- 実行:

```
make back-run
```

- 設定:
  - `PLAYERS_FILE`（任意）: プレイヤー一覧 JSON のパス（省略時は `backend/players.json`）。
  - `MATCH_LIMIT`（任意）: 直近試合何件を解析するか（デフォルト 10）。
  - `SKIP`（任意）: 一部リトライ抑制の簡易モード（`true`/`false`）。

- 出力:
  - `backend/team_result.json` にチーム分け結果を保存。

## Web API（詳細）
- 起動:

```
make back-run-app
```

- エンドポイント:
  - `GET /healthz` → 200 OK
  - `POST /analyze`
    - リクエスト例:

    ```json
    {
      "players": [
        {"gameName": "ふぇいかー", "tagLine": "JP1"},
        {"gameName": "しょうめいかー", "tagLine": "JP1"}
      ],
      "matchLimit": 10
    }
    ```

    - レスポンス例:

    ```json
    {
      "teamA": [{"name":"...","skill_score":123}],
      "teamB": [{"name":"...","skill_score":120}],
      "sumA": 615,
      "sumB": 602
    }
    ```

- 環境変数:
  - `RIOT_API_KEY`（必須）
  - `MATCH_LIMIT`（任意、整数）
  - `PORT`（任意、デフォルト `8080`）

注: API 実装はリクエスト量を抑えるため、CLI に比べ一部の詳細（平均マッチランク計算の完全版）を簡略化しています。CLI と同等にしたい場合は拡張可能です。

## フロントエンド（UI）
- 起動:

```
make front-dev
```

- API のエンドポイントは `VITE_API_BASE` で変更可能（未設定時は `http://localhost:8080`）。
- ログ貼り付け → 「登録」: 形式例「名前#タグがロビーに参加しました」。日本語タグにも対応。
- 登録 UI:
  - 最大 10 人まで追加。登録済みは読み取り専用の行表示（削除可）。
  - 10 人未満のときのみ、1 行の手動追加欄（GameName/TagLine）を表示。
  - 「解析」で Web API に投げ、チームを表示。

## Docker
- ローカル用イメージのビルド/起動（ソースをマウント）:

```
make docker-build-local
make docker-run-local
```

実行時はホストの `backend/` を `/data` にマウントし、`PLAYERS_FILE=/data/players.json` を渡します。チーム結果などの出力はホスト側 `backend/` に生成されます。

- 公開用イメージのビルド/起動（ローカルで試す）:

```
make docker-build-release
make docker-run-release
```

`backend/players.json` はイメージに含めません（`.dockerignore` 済）。必要に応じて実行時に `-v $(PWD)/backend:/data -e PLAYERS_FILE=/data/players.json` で渡してください。

### GHCR から取得して実行（例）

```
docker pull ghcr.io/<owner>/<repo>:latest
docker run --rm \
  --env-file backend/.env \
  -e PLAYERS_FILE=/data/players.json \
  -v $(pwd)/backend:/data \
  -w /data \
  ghcr.io/<owner>/<repo>:latest
```



## 注意事項 / 既知の制限
- Riot API のレートリミットにより、人数や `MATCH_LIMIT` に応じて時間がかかります（429 は `Retry-After` に従って待機）。
- チャンピオン名は Data Dragon の固定バージョン（例: `15.14.1`）を参照しています。必要に応じて更新してください。
- Web API は簡略化している箇所があります。CLI と完全一致のロジックが必要な場合は issue/PR でご相談ください。

## ライセンス

MIT
