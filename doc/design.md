# reviewd 設計

ローカル Mac に常駐し、GitHub 上のイベント（MVP はレビュー依頼）を検知して、
作業中ブランチから隔離された worktree でローカルの Claude Code を自動起動する CLI。

## 1. 全体構成

```text
launchd (StartInterval)
  └─ reviewd poll
       ├─ source  : gh CLI で GitHub の現在状態を取得
       ├─ store   : SQLite の処理済みカーソルと突き合わせて新規イベントを抽出
       ├─ rule    : rules.yaml でマッチング (event × repo × author × label × draft)
       └─ runner  : worktree 準備 → claude -p 実行 → 結果保存 → macOS 通知
```

非目標（意図的に持たない）:

- GitHub Actions 上での実行（`anthropics/claude-code-action` は要件を満たさないため不採用）
- PR への自動コメント投稿（外向きの副作用。Phase 2 で確認フロー付きで検討）
- GUI 自動化ツール（Raycast / Keyboard Maestro / BTT）への依存

## 2. ディレクトリ構成

```text
cmd/reviewd/            # エントリポイント
internal/cli/           # cobra サブコマンド定義
internal/config/        # config.yaml / rules.yaml のロードと検証
internal/model/         # Event / Run など横断的な型
internal/source/github/ # gh CLI ラッパ
internal/store/         # SQLite（処理済みカーソル・実行履歴）
internal/rule/          # イベント → アクションのマッチング
internal/worktree/      # mirror clone と git worktree の管理
internal/runner/        # claude -p の実行と結果保存
internal/notify/        # macOS 通知
internal/daemon/        # launchd plist の生成 / install / uninstall
doc/                    # 設計ドキュメント
```

## 3. ディスクレイアウト

| パス | 用途 |
| --- | --- |
| `~/.config/reviewd/config.yaml` | 全体設定 |
| `~/.config/reviewd/rules.yaml` | ルール定義 |
| `~/.reviewd/state.db` | SQLite（処理済みカーソル・実行履歴） |
| `~/.reviewd/repos/<owner>__<repo>.git` | bare mirror clone |
| `~/.reviewd/worktrees/<owner>__<repo>/<repo>-pr<N>` | レビュー用 worktree。末尾を `<repo>-pr<N>` にしているのは、ターミナルのタブがカレントディレクトリ名をタイトルにするため（`pr-42` だけではどのリポジトリか分からない） |
| `~/.reviewd/runs/<owner>__<repo>/pr-<N>/<ts>/` | 実行結果（`result.md` / `stream.jsonl` / `meta.json`） |
| `~/.reviewd/logs/` | launchd の stdout / stderr |
| `~/.reviewd/reviewd.lock` | poll の多重起動防止 |

`~` 起点なのは launchd から起動されても解決できるようにするため。全パスは config で上書き可能。

## 4. 設定ファイル

### 4-1. config.yaml

```yaml
version: 1

paths:
  work_dir: ~/.reviewd          # 以下は work_dir からの既定導出。個別に上書き可
  db_path: ""                   # 空なら <work_dir>/state.db
  runs_dir: ""
  logs_dir: ""

github:
  gh_path: gh
  # 空なら `gh api user` で解決してキャッシュする
  login: ""

claude:
  path: claude
  # headless（既定）: `claude -p` を子プロセスで実行して結果を保存する
  # ghostty        : Ghostty のタブで対話 claude を起動して即座に戻る（§8-2）
  # rules.yaml の action.mode でルール単位に上書き可
  mode: headless
  # 起動時に必ず渡すモデル。"default" は Claude Code の Default (recommended)。
  # --model はコマンドライン指定が settings.json の model や ANTHROPIC_MODEL より
  # 優先されるため、明示することで意図しないモデルでの起動を防げる。
  # 空にすると --model を渡さない（周囲の設定に従う）
  model: default
  # 1 実行あたりの上限。ルール側で上書き可
  timeout: 20m
  # 同時に走らせる claude プロセスの上限
  max_concurrent: 1
  # claude -p に必ず付ける引数（レビューを読み取り専用に閉じ込める）
  extra_args:
    - --output-format
    - stream-json
    - --verbose
    - --allowedTools
    - "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git show:*),Bash(git status:*),Bash(git rev-parse:*)"
    - --disallowedTools
    - "Edit,Write,NotebookEdit,Bash(git push:*),Bash(git commit:*),Bash(gh:*)"
  # 対話セッションを Remote Control 有効で起動する（既定 true）
  # headless では効かない（`claude -p` に操作対象のセッションが無い）
  remote_control: true
  # mode: ghostty のとき対話 claude に渡す引数。既定は空
  # （headless の締め付けは `-p` が許可を尋ねられないための措置。人がいる対話セッションには
  #   不要で、--disallowedTools は実行時に覆せないため Write / gh を塞ぐとプロンプトが壊れる）
  interactive_args: []

# mode: ghostty のときだけ使う
ghostty:
  app_path: /Applications/Ghostty.app
  window: front       # front = 最前面 window にタブを足す / new = 毎回新規 window
  activate: false     # true で新タブを前面に出す（既定 false: フォーカスを奪わない）
  keep_open: true     # claude 終了後もタブを残す

notify:
  enabled: true
  # terminal-notifier があれば優先し、なければ osascript にフォールバック
  prefer_terminal_notifier: true

poll:
  # 1 回の poll で起動を許すジョブ数の上限（暴走時のブレーキ）
  max_jobs_per_poll: 3
  lock_timeout: 5s
```

### 4-2. rules.yaml

```yaml
version: 1

rules:
  - name: review-requested
    enabled: true
    on: [review_requested]
    match:
      repos: ["YasushiKobayashi/*"]      # glob。空なら全許可
      exclude_repos: []
      authors: []                        # PR 作成者
      exclude_authors: []
      labels: []                         # いずれか 1 つでも一致
      exclude_labels: ["wip", "do-not-review"]
      include_draft: false
    action:
      prompt: |
        あなたはこの PR のレビュアーです。
        `git diff origin/{{.BaseRefName}}...HEAD` の差分をレビューし、
        以下を Markdown で出力してください。
        - 重大度 high の指摘のみ。nit は書かない
        - 各指摘に file:line と、なぜ問題かの 1 行
        - 指摘がなければ「指摘なし」とだけ書く
      timeout: 20m
      notify: true
      # 省略時は reviewd が worktree を用意する。指定するとそのディレクトリで
      # claude を起動し、reviewd は worktree を一切用意しない（§7-1）
      # workdir: ~/go/src/github.com/owner/repo
```

`prompt` と `workdir` は Go の `text/template` で展開する。利用可能な変数は §5 の `Event` の公開フィールド。

`match.*` のパターン構文（`internal/glob`）:

| 記法 | 意味 |
| --- | --- |
| `*` | `/` 以外の任意の並び |
| `**` | `/` を含む任意の並び |
| `?` | `/` 以外の 1 文字 |
| その他 | すべてリテラル（正規表現メタ文字も literal 扱い） |

大文字小文字は区別しない（GitHub の login / repo / label がそうであるため）。
無効なパターンは存在しないので compile は失敗しない。
`repos` を空にすると「制約なし」、`YasushiKobayashi/*` は 1 階層のみ、全許可を明示したいなら `**`。
`exclude_*` は常に対応する許可リストより優先される。

**意思決定**: `/code-review` skill に丸投げせず rules.yaml に観点を書く方式を採用した。
ルール単位で観点を差し替えられる（レビュー依頼と CI 失敗で別プロンプトにできる）ことを優先したため。
`prompt: "/code-review high"` と書けば skill 呼び出しにもできるので上位互換になっている。

## 5. イベントモデル

```go
type Kind string

const (
    KindReviewRequested Kind = "review_requested"
    // Phase 2: review_submitted / new_commit / check_completed / dependency_update
)

type Event struct {
    Kind        Kind
    Repo        string // "owner/name"
    Owner       string
    Name        string
    PRNumber    int
    PRTitle     string
    PRURL       string
    Author      string
    HeadRefName string
    HeadSHA     string
    BaseRefName string
    IsDraft     bool
    Labels      []string

    // 冪等キーの値。§6 参照
    Cursor     string
    DetectedAt time.Time
}
```

### 5-1. review_requested の取得

```sh
gh search prs --review-requested=@me --state=open --json \
  number,title,url,repository,author,isDraft,labels --limit 50
```

`gh search prs` は head SHA / base ref を返さないため、ヒットした PR ごとに 1 回だけ追加取得する。

```sh
gh pr view <N> --repo <owner>/<name> --json headRefName,headRefOid,baseRefName,isDraft,labels,author,title,url
```

呼び出し回数は「レビュー依頼中の open PR 数」に比例する。通常は数件なので許容。
ヒット 0 件のときは追加呼び出しゼロ。

## 6. 冪等性

`Cursor` の値は **head SHA** を使う。

- 同じ PR に対してレビュー依頼が出ている間、**新しい commit が積まれるたびに 1 回だけ** 走る
- 単なる `poll` の繰り返しでは再実行されない
- PR node id 固定にすると「修正 push 後にもう一度見たい」が拾えないため採用しなかった

ノイズが問題になった場合は rules.yaml に `min_interval` を足して抑制する（Phase 2）。

### 6-1. スキーマ

```sql
CREATE TABLE IF NOT EXISTS processed (
    repo         TEXT    NOT NULL,
    pr_number    INTEGER NOT NULL,
    kind         TEXT    NOT NULL,
    cursor       TEXT    NOT NULL,
    processed_at TEXT    NOT NULL,
    PRIMARY KEY (repo, pr_number, kind, cursor)
);

CREATE TABLE IF NOT EXISTS runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo        TEXT    NOT NULL,
    pr_number   INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    cursor      TEXT    NOT NULL,
    rule        TEXT    NOT NULL,
    status      TEXT    NOT NULL, -- running / success / failed / timeout / skipped / launched
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    exit_code   INTEGER,
    output_dir  TEXT,
    error       TEXT
);

CREATE INDEX IF NOT EXISTS idx_runs_started_at ON runs(started_at DESC);
```

`processed` への記録は **claude の実行結果に関わらず、起動を試みた時点で** 行う。
失敗のたびに無限リトライして API とトークンを溶かす事故を防ぐため。再実行は `reviewd run` で手動。

### 6-2. seed

初回導入時に既存のレビュー依頼が一斉に発火する事故を防ぐため、
`reviewd seed` は「現時点で検知できるイベントを、実行せずに `processed` へ記録するだけ」を行う。

## 7. worktree

作業中のローカルリポジトリを **一切触らない** ため、専用の bare mirror を持つ。

```sh
# 初回
gh repo clone <owner>/<name> ~/.reviewd/repos/<owner>__<repo>.git -- --bare
# 毎回
git --git-dir=<mirror> fetch --prune origin '+refs/heads/*:refs/remotes/origin/*'
git --git-dir=<mirror> worktree add --detach <worktree-path> <headSHA>
```

- worktree のパスは `~/.reviewd/worktrees/<owner>__<repo>/<repo>-pr<N>`。
  末尾を `<repo>-pr<N>` にしているのは、ターミナル（Ghostty）のタブがカレントディレクトリ名を
  タイトルにするため。`pr-42` だけでは開いているタブがどのリポジトリのものか分からない
- worktree は実行後に削除する（`git worktree remove --force`）。`action.keep_worktree` で残せる。
  ghostty モードでは指定に関わらず残す（§8-2）
- mirror は再利用する（毎回 clone しない）
### 7-1. `action.workdir`: 既存 clone で起動する

ルールに `action.workdir` を書くと、**reviewd は worktree を一切用意せず**、そのディレクトリで
claude を起動する。指定パスが存在しない / ディレクトリでない場合は `RunFailed` にする
（黙って worktree にフォールバックすると、この設定を入れた目的が失われるため）。

**なぜ必要か（隔離より優先される事情）**: Claude Code の設定の一部は
**セッションを開始したディレクトリに紐づく**。

| 設定 | 保存場所 | worktree から見えるか |
| --- | --- | --- |
| MCP サーバー（`local` スコープ。`claude mcp add` の既定） | `~/.claude.json` の `projects["<パス>"].mcpServers` | ❌ 見えない |
| `.mcp.json`（`project` スコープ）の承認 | `~/.claude.json` の `projects["<パス>"].enabledMcpjsonServers` | ❌ 毎回聞かれる |
| `.mcp.json` 自体 | リポジトリに commit | ✅ ある |

reviewd が作る worktree は **PR ごとに新しいパス**なので、承認が永久に貯まらず毎回
セットアップ確認が出る。加えて `local` スコープの MCP サーバーは 1 本も見えない。
`user` スコープへ移せば解決するが、リポジトリ単位で MCP を管理したい運用とは相容れない。

そこで **claude は既存 clone で起動し、隔離は claude 自身が worktree を作ることで担保する**。
プロンプト側に手順を書く（`git worktree add --detach ../<repo>-pr<N> <headSHA>` → `cd`）。

**トレードオフ**: この方式では、ユーザーの clone の `.git` に PR の ref と worktree の
管理情報が書き込まれる。「作業中のリポジトリを一切触らない」という当初の要件は
**現在のブランチと作業ツリーには触らない**、まで緩む。fetch と `git worktree add` は
HEAD も working tree も変更しないため、実害はない範囲と判断した。

## 8. runner

実行モードは `claude.mode`（rules.yaml の `action.mode` でルール単位に上書き可）で決まる。
`headless` が既定で、`ghostty` は §8-2。

### 8-1. headless

1. worktree を用意
2. `cd <worktree>` で `claude -p <prompt> <extra_args...>` を実行
3. stdout を `stream.jsonl` にそのまま保存
4. stream-json の最終 `result` イベントからテキストを取り出して `result.md` に保存
5. `meta.json` にイベント・ルール・所要時間・exit code を保存
6. `runs` テーブルを更新

**権限**: `--permission-mode plan` は使わない。plan モードは「実行せず計画を返す」挙動なので、
レビュー本文が返ってこない。代わりに `--allowedTools` で読み取り系ツールだけを許可し、
`--disallowedTools` で編集・push・gh 操作を明示的に塞ぐ。`-p` では許可されていないツールは
プロンプトを出せないため自動的に拒否される。

制御:

- `context.WithTimeout` でルールの `timeout` を適用。超過時は プロセスグループごと kill し `status=timeout`
- `max_concurrent` を semaphore で担保
- `max_jobs_per_poll` を超えた分はその poll では起動せず、`processed` にも記録しない（次の poll で拾う）

### 8-2. ghostty

Ghostty のタブで**対話モードの** claude を起動し、タブが出来た時点で戻る。
「レビュー依頼を検知したら、その PR の worktree で claude が起動済みのタブが勝手に生えている」
状態を作るためのモード。出力の回収はしない（読むのは人）。

```text
poll → rule → worktree 用意 → prompt.md 書き出し → osascript で new tab → 即 return
```

生成する AppleScript（Ghostty 1.3 の scripting dictionary）:

```applescript
tell application "Ghostty"
  set cfg to new surface configuration
  set command of cfg to "/bin/sh -c '...'"
  set initial working directory of cfg to "<worktree>"
  set environment variables of cfg to {"REVIEWD_REPO=...", ...}
  set wait after command of cfg to true
  if (count of windows) is 0 then
    set w to new window with configuration cfg
    set t to selected tab of w
  else
    set t to new tab in window 1 with configuration cfg
  end if
  return (id of t) & tab & (id of trm)
end tell
```

設計上の決定:

- **`new tab` には必ず `in <window>` を付ける。** 省略すると `-1708 (Can't continue new tab)` を返すが、
  **その時点でタブは作られコマンドも走っている**。素朴に再試行すると同じジョブが二重に開く。
  window が 0 個のときだけ `new window` にフォールバックする。
- **プロンプトはファイル経由で渡す。** タブが実行するのは
  `/bin/sh -c 'exec claude <args> "$(cat <run-dir>/prompt.md)"'`。
  レビュープロンプトは複数行でバッククォートも引用符も含むため、AppleScript リテラルと
  シェルの二重引用をくぐらせるより、ファイルに書いて内側の sh に読ませる方が壊れない。
  引用は 2 層（`ShellQuote` → `appleScriptString`）とも実シェル・実パーサで往復テストしている。
- **worktree を掃除しない。** claude はまだそのディレクトリを使っており、reviewd は終了を知る手段がない。
  `action.keep_worktree` の指定に関わらず残す。**溜まった分の掃除は自動化しない**（ユーザー判断 2026-09-10）。
  `~/.reviewd/worktrees/` は手で消す。作業中のタブが使っているディレクトリを
  自動削除で消し飛ばすリスクを取るより、溜まったら手で消す方が安全という判断。
- **status は `launched`。** reviewd から見た終端状態。成否は原理的に分からない。
- **タブの画面内容は読めない。** `terminal` クラスのプロパティは `id` / `name` / `working directory` の 3 つだけで、
  スクリーンバッファを取る API が無い。出力を回収したいなら tmux（`capture-pane`）か headless を使う。
- **`wait after command` を既定で true にする。** 起動直後に落ちた場合にタブごと消えると原因が追えない。
- **対話セッションにはツール制限を掛けない**（`interactive_args` の既定は空）。
  headless の `--allowedTools` / `--disallowedTools` は `-p` が許可を尋ねられない
  （未許可ツールは黙って拒否される）ための措置であって、人がいる対話セッションには要らない。
  むしろ `--disallowedTools` は実行時に覆せないため、ここで `Write` や `gh` を塞ぐと
  「タスクファイルを残す」「PR を読む」タイプのプロンプトが壊れる。
- **Remote Control を既定で有効にする**（`claude.remote_control`、既定 true）。
  `--remote-control reviewd-<owner>__<name>-pr<N>` を付けて起動するので、
  reviewd がこの Mac で開いたレビューを別デバイスから操作できる。
  セッション名を PR ごとに付けるのは、機械上の他のセッションと区別できないと
  Remote Control の意味が無いため。headless には付けない（`-p` に操作対象のセッションが無い）。

**TCC（自動化許可）**: `osascript` から Ghostty を操作するため、呼び出し元プロセスに
「Ghostty を制御」の許可が要る。launchd から起動した reviewd は別プロセス扱いで改めて許可が必要になり、
バックグラウンド起動ではダイアログが出ないまま `-1743 Not authorized` で失敗しうる。
`daemon install` 後に一度手で `reviewd run --mode ghostty` を実行して許可を取る運用とする。

## 9. 通知

`terminal-notifier` があれば優先（結果ディレクトリを開けるため）:

```sh
terminal-notifier -title "reviewd" -subtitle "<repo>#<pr>" -message "<summary>" \
  -open "file://<runs-dir>/result.md"
```

なければ `osascript -e 'display notification ...'` にフォールバック。
通知は失敗しても run 全体は失敗させない（best-effort）。

## 10. CLI

| コマンド | 説明 |
| --- | --- |
| `reviewd poll` | 1 回ポーリングして条件に合うものを実行（launchd から呼ばれる） |
| `reviewd poll --dry-run` | 検知とマッチングだけ行い、実行も記録もしない |
| `reviewd seed` | 現在検知できるイベントを実行せず処理済みとして記録 |
| `reviewd run --repo <o/r> --pr <N> [--rule <name>] [--mode <mode>]` | 手動で 1 本実行（`processed` を無視）。`--mode` で headless / ghostty を上書き |
| `reviewd status [--limit N]` | 直近の実行履歴 |
| `reviewd rules validate` | 設定ファイルの検証 |
| `reviewd daemon install \| uninstall \| status` | launchd への登録 |

## 11. launchd

`~/Library/LaunchAgents/com.yasushikobayashi.reviewd.plist` を生成する。

- `ProgramArguments`: `<絶対パス>/reviewd poll`
- `StartInterval`: 180（既定）
- `RunAtLoad`: true
- `StandardOutPath` / `StandardErrorPath`: `~/.reviewd/logs/`
- `EnvironmentVariables.PATH`: `gh` / `claude` / `git` を解決できる PATH を install 時に埋め込む
  （launchd の既定 PATH には aqua の shim が含まれないため、ここが最頻の詰まりどころ）

多重起動は `~/.reviewd/reviewd.lock` の flock で防ぐ。取得できなければ即座に exit 0。

## 12. Phase 2 以降

- イベント種別の拡張: `review_submitted` / `new_commit` / `check_completed` / Renovate 等の更新
- 結果の PR コメント投稿（投稿前確認フロー付き）
- tmux セッションを立てて対話モードの claude を起動する出力先
  （Ghostty タブ版は §8-2 で実装済み。tmux 版は出力を `capture-pane` で回収できる点が違う）
- Slack 通知
- `repo_source: local`（既存 clone の流用）
- `min_interval` によるノイズ抑制

`reviewd gc`（古い mirror / worktree / runs の自動削除）は **やらない**（ユーザー判断 2026-09-10）。
`~/.reviewd/` が溜まったら手で消す。
