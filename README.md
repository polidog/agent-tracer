# agent-tracer

> 旧称 `skill-logger`。MCP ツール記録対応 (v0.2) のタイミングで改名しました。
> 旧バイナリ名 (`skill-logger record`) と旧 config (`~/.skill-logger/`) /
> 旧 env (`SKILL_LOGGER_*`) は当面 fallback として有効です。

Claude Code（および Codex）で使った **Skill** / **slash command** / **MCP ツール**の
利用状況を SQLite/Turso に記録し、ターミナル UI で集計を眺めるためのツール。

- `agent-tracer`        … Bubble Tea 製の TUI で 9 つのビューを切替表示
  - Skills / Commands / MCP / Projects / Hosts / Users / Daily / Recent
- `agent-tracer record` … hook から渡された JSON を読んで 1 件記録する
- `agent-tracer stats`  … ランキング / 日次タイムラインを stdout に出す
- `agent-tracer sync`   … Turso embedded replicas を手動同期（ローカル SQLite では no-op）

データは既定で `~/.agent-tracer/events.db` に保存される。設定は
`~/.agent-tracer/config.toml`（任意）で変更可。

## インストール

`go-libsql` を内部で使うため CGO が必要:

```sh
CGO_ENABLED=1 go install github.com/polidog/agent-tracer@latest
```

## クイックスタート: hook 設定の自動セットアップ

`agent-tracer init` で Claude Code (`~/.claude/settings.json`) と Codex
(`~/.codex/config.toml`) 両方の hook 設定を一発で入れられる。**既存の設定は
上書きせずマージする** (agent-tracer 由来エントリが無ければ追加、あれば何もしない
ので idempotent)。

```sh
# 推奨スニペットを stdout に表示するだけ (安全側のデフォルト)
agent-tracer init

# 実際にファイルへ反映 (両方)
agent-tracer init --write

# Claude だけ / Codex だけ
agent-tracer init --target claude --write
agent-tracer init --target codex  --write

# パスを上書きしたい場合
agent-tracer init --write \
  --claude-settings ~/my-claude/settings.json \
  --codex-config   ~/my-codex/config.toml
```

`--write` は既存ファイルをパースして hook 配列に agent-tracer エントリを **追加** する。
既に `agent-tracer record` を含むコマンドがそのイベントに登録されていればスキップ
するので、何度実行しても重複しない。

注意: 書き込み時にファイルを再シリアライズするため、JSON のキー順や TOML の
コメントは保持されない場合がある。先に `agent-tracer init` (引数なし) で
スニペットを確認し、手動編集の方が安心なときは copy & paste しても良い。

## Config (`~/.agent-tracer/config.toml`)

config ファイルは **任意** で、無ければローカル SQLite モードで動く。
Turso の Embedded Replicas を使いたいときだけ作成する。

```toml
# "local" (default) または "turso"
mode = "turso"

# 省略時は <AGENT_TRACER_DIR>/events.db (= ~/.agent-tracer/events.db)
db_path = "~/.agent-tracer/events.db"

# 端末を識別するラベル。複数端末で Turso を共有しているときに
# どの端末で記録されたか判別するのに使う。省略すると os.Hostname() が入る。
hostname = "macbook-work"

# 個人を識別するラベル (チーム共有時の集計キー)。省略すると
# `git config --get user.email` の値が入る。匿名にしたいなら ""。
user = "polidog@example.com"

# 共有 DB に prompt 全文 (raw 列) を含めるかどうか。デフォルト true。
# チーム共有 Turso では false にして prompt を伏せるのが安全。
share_raw = false

[turso]
url = "libsql://<your-db>.turso.io"
auth_token = "..."           # env TURSO_AUTH_TOKEN が優先
sync_interval = "60s"        # 省略すると手動 sync のみ
```

設定の優先順位:

| 優先 | ソース | 説明 |
| --- | --- | --- |
| 1 | `--db` / `--config` / `--user` / `--host` CLI フラグ | コマンド単位で上書き |
| 2 | 環境変数 (`AGENT_TRACER_DB`, `AGENT_TRACER_HOSTNAME`, `AGENT_TRACER_USER`, `AGENT_TRACER_SHARE_RAW`, `TURSO_DATABASE_URL`, `TURSO_AUTH_TOKEN`) | hook やシェルで一時的に切替 |
| 3 | `config.toml` | 通常の永続設定 |
| 4 | デフォルト | mode=local, `~/.agent-tracer/events.db`, user=`git config user.email`, share_raw=true |

`TURSO_DATABASE_URL` がセットされていると、config に `mode` が無くても自動的に turso モードになる。

## チームで共有して使う

Turso の Embedded Replica を team 全員で 1 つの DB に向ければ、誰がどの skill を
よく使っているか、最近チーム内で流行り始めた skill は何か、といったことを
集計できる。最低限以下の 3 つの仕組みが用意されている:

### 1. `user` 列で個人を識別

`host` (= 端末名) だけだと「同一人物の複数端末」と「別人」を区別できないので、
`user` 列も別途記録している。値の解決順は **`--user` フラグ → `AGENT_TRACER_USER`
→ `config.toml` の `user` → `git config --get user.email`**。何も無ければ空文字で
匿名イベントになる。

```toml
# ~/.agent-tracer/config.toml
user = "alice@example.com"
```

### 2. `share_raw = false` で prompt を伏せる

`raw` 列には hook が受け取った JSON 全文 (= ユーザーの prompt 含む) が入る。
ローカル運用ならデバッグに便利だが、共有 Turso に流すと他メンバーから prompt が
見えてしまう。`share_raw = false` をセットしておけば record 時に raw を空文字で
保存するので、共有 DB には kind / name / duration / token のメタデータだけが
残る。

```toml
share_raw = false
```

過去に取った raw を後から削除したい場合は `sqlite3 events.db "UPDATE events SET raw=''"`
など手動で消す必要がある (今のところ purge コマンドは用意していない)。

### 3. `--by user` / `--user <addr>` でチーム集計

```sh
# チーム全体で誰が一番多く skill を呼んでいるか
agent-tracer stats --by user

# 自分の skill ランキング (絞り込み)
agent-tracer stats --user alice@example.com --kind skill

# 直近 7 日でチーム内で最も使われた command
agent-tracer stats --kind command --since 7d
```

TUI には Users ビュー (`5` キー) と user フィルタ chip (`u` キーで巡回) が追加
されていて、Host と同じ感覚で個人軸の集計を眺められる。

### 推奨セットアップ (チーム共有)

各メンバーの `~/.agent-tracer/config.toml`:

```toml
mode = "turso"
hostname = "alice-macbook"
user = "alice@example.com"   # 省略しても git の user.email が拾われる
share_raw = false             # チーム共有時はオフを推奨

[turso]
url = "libsql://team-agent-tracer.turso.io"
auth_token = "..."            # 全員が同じ DB の token を持つ
sync_interval = "60s"
```

`hook` 設定は `agent-tracer init --write` で各自セットアップ。あとは記録が
自動で Turso に流れて、`stats --by user` や TUI で誰でも集計を眺められる。

## Claude Code の hook 設定

`~/.claude/settings.json` の `hooks` に以下を追加すると、Skill 呼び出しと
slash command 投入を自動で記録できる。`agent-tracer` は失敗しても exit 0 を
返すので、hook が Claude Code 本体をブロックすることはない。

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Skill|mcp__.*",
        "hooks": [
          { "type": "command", "command": "agent-tracer record --quiet" }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Skill|mcp__.*",
        "hooks": [
          { "type": "command", "command": "agent-tracer record --quiet" }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          { "type": "command", "command": "agent-tracer record --quiet" }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "agent-tracer record --quiet" }
        ]
      }
    ]
  }
}
```

`agent-tracer record` は stdin の JSON を見て自動的に種別を判定する:

| Hook event | 動作 |
| --- | --- |
| `PreToolUse` + `tool_name=Skill` | INSERT `kind=skill`, `name=tool_input.skill` (Claude Code のみ) |
| `PreToolUse` + `tool_name=mcp__<server>__<tool>` | INSERT `kind=mcp`, `name=<server>/<tool>` (ignore リストにマッチした場合は skip) |
| `UserPromptSubmit` (prompt が `/` 始まり) | INSERT `kind=command`, `name=<最初のトークン>` |
| `UserPromptSubmit` で `$<name>` mention あり (Codex のみ) | mention ごとに INSERT `kind=skill`, `name=<mention 名>` |
| `PostToolUse` + `tool_name=Skill` または `mcp__*` | INSERT した skill / mcp 行に `duration_ms` と token usage を書き込み |
| `Stop` | 同 session の未完了 command / skill / mcp 行に `duration_ms` と token usage を書き込み |

`duration_ms` は INSERT から finalize までのウォールタイム (ミリ秒)。token usage は
`transcript_path` で渡される JSONL の最新 assistant メッセージから
`input_tokens` / `cache_read_input_tokens` / `cache_creation_input_tokens`
/ `output_tokens` を抽出する。同じ finalize タイミングで利用モデルも記録される —
Claude Code は assistant メッセージの `model`、Codex は rollout の直近
`turn_context` の `model` を `model` 列に書き込む。

PostToolUse / Stop の hook を入れ忘れても INSERT は機能する (duration と token は 0 のまま) ので、
後から段階的に有効化してもよい。

それ以外の payload は無視 (exit 0)。`--source codex` で source 列を切り替えると
**追加で** Codex 固有の `$<name>` mention を skill として記録する経路が有効になる
(Codex には Claude Code のような `Skill` ツールが無く、Skill 本文はプロンプトに
直接注入されるため)。Codex の hook 設定例は下の [Codex の hook 設定](#codex-の-hook-設定) を参照。

### skill と command の判定について

Claude Code では Skills と slash command は内部的に統合されており、
`.claude/skills/<name>/SKILL.md` と `.claude/commands/<name>.md` のどちらで定義しても
同じ `/<name>` で呼び出せる。一方で **起動経路は分かれている** ため、
agent-tracer では次のように記録される。

| 起動経路 | 発火する hook | agent-tracer の記録 |
| --- | --- | --- |
| Claude が `Skill` ツール経由で起動 (`SKILL.md` 形式 + 自動起動) | `PreToolUse` (`tool_name=Skill`) | `kind=skill` |
| ユーザーが `/<name>` をプロンプトに直接入力 (commands 形式のプロンプト展開) | `UserPromptSubmit` | `kind=command` |

そのため、同じ名前のスキル/コマンドでも、どう呼び出されたかによって `kind` が
変わる場合がある (例: `/dev` がプロンプト入力経由なら command として記録される)。
これは Claude Code 側の挙動をそのまま反映したものなので、agent-tracer としては
両方の経路を別々の利用イベントとして残す方針にしている。

### MCP ツールの記録と ignore 設定

Claude Code は MCP サーバーのツールを `tool_name = mcp__<server>__<tool>` の形で
Pre/PostToolUse hook に流す。`init` が書き込む matcher は
`Skill|mcp__.*` の正規表現で、Skill と MCP 両方を 1 つのエントリで拾う。

events.name は `<server>/<tool>` (例: `claude-in-chrome/tabs_context_mcp`) で
保存され、TUI の MCP タブで集計できる。MCP ツールは Skill より呼び出し頻度が
高いことが多いので、不要なサーバーは `~/.agent-tracer/config.toml` の `[mcp]`
セクションで除外できる:

```toml
[mcp]
ignore = [
  "claude_ai_Gmail",     # サーバー名だけ書くと配下の全ツール
  "figma/*",             # glob (path.Match) で `figma/<tool>` を全部除外
  "*/authenticate",      # tool 名だけマッチさせるパターン
]
```

ignore にマッチした PreToolUse は INSERT をスキップする。
(PostToolUse は対応する INSERT が無ければ無条件で no-op になるので、
ignore 側の追加/変更だけで完結する。)

## Codex の hook 設定

Codex CLI も `~/.codex/config.toml` で command 型 hook を登録できる。Codex の
JSON payload は Claude Code とフィールド名がほぼ一致しているため、同じ
`agent-tracer record` バイナリをそのまま噛ませられる。`--source codex` を
付けることで、`$skill-name` mention が skill として記録されるようになる。

```toml
[[hooks.UserPromptSubmit]]

[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "agent-tracer record --quiet --source codex"

[[hooks.Stop]]

[[hooks.Stop.hooks]]
type = "command"
command = "agent-tracer record --quiet --source codex"
```

| 起動経路 (Codex) | 発火する hook | agent-tracer の記録 |
| --- | --- | --- |
| TUI で `$skill-name` mention (または `/skills` で選択) | `UserPromptSubmit` | `kind=skill` (mention 1 件につき 1 行) |
| 組み込み slash command (`/plan` 等) を入力 | `UserPromptSubmit` (発火する場合) | `kind=command` |

Codex には Claude Code のような `Skill` ツールは存在せず、Skill 本文はプロンプト
コンテキストに直接注入される設計 (cf. `codex-rs/core-skills`)。そのため Codex 側の
skill 起動は `$skill-name` mention 検出経由で記録される。`PostToolUse Skill` は
発火しないので、duration / token は **`Stop` hook で turn 終了時にまとめて埋める**
仕組み (同 session で pending な command + skill 行をすべて finalize する) になっている。
ターン内に複数 mention があった場合、各行に同じ token usage 値が乗り、duration_ms は
mention 検出時刻から `Stop` 受信までの実時間が個別に記録される。

非 Codex の prompt 内に偶然 `$word` が現れても skill 化されないよう、mention の
検出は `--source codex` 指定時のみ有効。`$PATH` / `$HOME` などよくある環境変数も
スキップされる (Codex 本体と同じ除外リスト)。

### Codex の token usage マッピング

Codex の rollout JSONL (`~/.codex/sessions/rollout-*.jsonl`) には `token_count` イベントが
記録されており、これを `transcript_path` 経由で読み取って agent-tracer の token 列に流し込む。
Codex は cache の生成/読み込みを区別しないため、以下のように Claude Code の 4 列に正規化する:

| Codex の値 | agent-tracer の列 | 備考 |
| --- | --- | --- |
| `last_token_usage.input_tokens - cached_input_tokens` | `input_tokens` | 非キャッシュ入力 |
| `last_token_usage.cached_input_tokens` | `cache_read_tokens` | キャッシュヒット |
| (Codex に該当概念なし) | `cache_creation_tokens` | 常に 0 |
| `last_token_usage.output_tokens` | `output_tokens` | reasoning トークン込み |

`stats` / TUI の context 列 (`input + cache_read + cache_creation`) は Codex 値の
`input_tokens` と一致するので、Claude Code と Codex を混在した集計でも一貫した
意味で context size を比較できる。

## 使い方

### TUI

```sh
agent-tracer
```

- `tab` / `←` `→` / `1`–`9`: ビュー切替 (Skills / Commands / MCP / Projects / Hosts / Users / Models / Daily / Recent)
- `↑` `↓` または `j` `k`: 行移動
- `enter`: Recent タブで選択イベントの詳細表示 (model・token 内訳・session など、`esc` で閉じる)
- `r`: 再読込
- `f`: 期間切替 (All / 7d / 24h)
- `s`: source 切替 (All / Claude / Codex)
- `m`: host 切替 (All / 各端末)
- `u`: user 切替 (All / 各メンバー)
- `o`: model 切替 (All / 各モデル)
- `q` または `Ctrl+C`: 終了

### CLI 集計

```sh
# Skill ランキング上位 20 件
agent-tracer stats --kind skill --limit 20

# 直近 7 日の slash command ランキング
agent-tracer stats --kind command --since 7d

# プロジェクト (cwd) 別ランキング
agent-tracer stats --by project

# 端末 (host) 別ランキング
agent-tracer stats --by host

# 特定端末だけに絞る
agent-tracer stats --host macbook-work

# モデル別ランキング / 特定モデルに絞る
agent-tracer stats --by model
agent-tracer stats --model claude-fable-5

# 日次タイムライン (全件)
agent-tracer stats --daily
```

`--by project` を付けると hook が記録した `cwd` ごとに集計する。表示時に
`git rev-parse --show-toplevel` を試してリポジトリのルートにまとめ、`$HOME`
配下は `~` 短縮で表示する (DB は cwd 生値のまま)。

`--by host` / `--host <name>` は端末名で集計・絞り込みする。記録時の端末名は
`config.hostname` → 環境変数 `AGENT_TRACER_HOSTNAME` → `os.Hostname()` の順に
解決される。Turso で複数端末から書き込むときは config に分かりやすい
hostname を入れておくと混ざらない。

`--since` は `30m` / `24h` / `7d` / `2w` のようなショートハンドか RFC3339 を受け付ける。

## データベーススキーマ

```sql
CREATE TABLE events (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                    TEXT NOT NULL,       -- RFC3339Nano (UTC) — INSERT 時刻
  source                TEXT NOT NULL,       -- claude | codex
  kind                  TEXT NOT NULL,       -- skill | command | mcp
  name                  TEXT NOT NULL,
  session_id            TEXT NOT NULL DEFAULT '',
  cwd                   TEXT NOT NULL DEFAULT '',
  host                  TEXT NOT NULL DEFAULT '',  -- 端末名
  "user"                TEXT NOT NULL DEFAULT '',  -- 個人識別子 (デフォルトは git config user.email)
  raw                   TEXT NOT NULL DEFAULT '',  -- 元の hook JSON (share_raw=false で空に)
  tool_use_id           TEXT NOT NULL DEFAULT '',  -- skill の PreToolUse→PostToolUse 対応用
  duration_ms           INTEGER NOT NULL DEFAULT 0,  -- INSERT→finalize の経過 ms (0 = 未確定)
  model                 TEXT NOT NULL DEFAULT '',    -- finalize 時に transcript から取得 ('' = 未確定)
  input_tokens          INTEGER NOT NULL DEFAULT 0,  -- 以下 transcript の最新 usage
  output_tokens         INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
  cache_creation_tokens INTEGER NOT NULL DEFAULT 0
);
```

旧スキーマは起動時に冪等な `ALTER TABLE` で自動マイグレーションされる。
コンテキスト総入力量は `input_tokens + cache_read_tokens + cache_creation_tokens` で算出可能。

## ライセンス

MIT
