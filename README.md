# reviewd

Mac に常駐して GitHub のレビュー依頼を検知し、**作業中のブランチから隔離された worktree で
ローカルの Claude Code を自動起動する** CLI。

GitHub Actions（`anthropics/claude-code-action`）ではなく、**手元の PC で動かす**ことが前提。

```text
launchd (StartInterval)
  └─ reviewd poll
       ├─ gh CLI で GitHub の状態を取得
       ├─ SQLite の処理済みカーソルと比較して新規イベントを抽出
       ├─ rules.yaml でマッチング (event × repo × author × label × draft)
       └─ git worktree を用意 → claude -p 実行 → 結果を保存 → macOS 通知
```

設計の詳細は [doc/design.md](doc/design.md)。

## 必要なもの

| ツール | 用途 | 備考 |
| --- | --- | --- |
| macOS | launchd / 通知 | Linux でもコマンドは動くが `daemon` と通知は macOS 前提 |
| Go 1.27+ | ビルド | |
| [`gh`](https://cli.github.com/) | レビュー依頼の検知・private repo の clone | `gh auth login` 済みであること |
| `git` | worktree | |
| `claude` | レビュー本体 | ログイン済みであること |
| `terminal-notifier` | 通知（任意） | 無ければ `osascript` にフォールバック。あるとクリックで `result.md` を開ける |

## インストール

main への push でテストが通ると、CI が tag を切って darwin arm64 / amd64 のバイナリを
[Releases](https://github.com/YasushiKobayashi/agent-dispatch/releases) に上げる。tag 解決と checksum 検証をまとめて扱えるので `gh` で取得する。

```sh
gh release download --repo YasushiKobayashi/agent-dispatch --pattern '*darwin_arm64.tar.gz' --dir /tmp/reviewd --clobber
tar -xzf /tmp/reviewd/*darwin_arm64.tar.gz -C /tmp/reviewd reviewd
install -m 0755 /tmp/reviewd/reviewd ~/.local/bin/reviewd
reviewd --version
```

ソースからビルドする場合:

```sh
make build                      # ./bin/reviewd
make install                    # ~/.local/bin/reviewd へコピー
```

### リリースのバージョン

`scripts/next-version.sh` が前回の `vX.Y.Z` tag 以降の commit（Conventional Commits）から決める。

| commit | 上がる桁 |
| --- | --- |
| `feat!:` / `fix(scope)!:` 等、または本文に `BREAKING CHANGE:` | major |
| `feat:` / `feat(scope):` | minor |
| それ以外（`fix:` / `chore:` / `docs:` / merge commit 等） | patch |

tag が 1 つも無いときは `v0.1.0`。HEAD が既に tag 付きならリリースしない。

## セットアップ

```sh
# 1. 設定ファイルの雛形を作る
reviewd init
#    macOS: ~/Library/Application Support/reviewd/{config,rules}.yaml
#    Linux: ~/.config/reviewd/{config,rules}.yaml
#    （置き場所は --config / --rules で上書きできる）

# 2. rules.yaml を編集（対象リポジトリ・レビュー観点のプロンプトなど）
$EDITOR ~/Library/Application\ Support/reviewd/rules.yaml

# 3. 何が拾われるか確認する（実行も記録もしない）
reviewd poll --dry-run

# 4. 溜まっているレビュー依頼を「処理済み」にして、初回の一斉発火を防ぐ
reviewd seed

# 5. launchd に登録
reviewd daemon install
```

`daemon install` は **その時点で解決した PATH を plist に焼き込む**。launchd の既定 PATH には
aqua / Homebrew / mise の shim が入らず `gh`・`claude`・`git` が見つからないため。
これらを入れ替えたら `reviewd daemon install` をやり直すこと。

PATH だけでは足りないので、次の環境変数も一緒に焼き込む（設定されているものだけ）。

- `AQUA_GLOBAL_CONFIG` ほか aqua 系 — aqua の shim は PATH 上にあっても、これが無いと
  aqua 自身が `gh: command is not found` を返す
- `XDG_CONFIG_HOME` / `GH_CONFIG_DIR` — gh は `hosts.yml` をここから探す。`XDG_CONFIG_HOME`
  を `~/.config` 以外に置いている環境では、これが無いと launchd 配下の gh だけが
  **「`gh auth login` を実行してください」** になる（ターミナルでは通っているのに）
- `XDG_DATA_HOME` / `XDG_CACHE_HOME` / `CLAUDE_CONFIG_DIR`

同じ環境は `mode: ghostty` のタブにも渡している。Ghostty は launchd が起動する GUI アプリで
PATH が `/usr/bin:/bin:/usr/sbin:/sbin` しか無く、タブのコマンドはログインシェルを経由しないため、
素の `claude` では **セッションが始まる前に `claude: not found` で落ちる**。

## コマンド

| コマンド | 説明 |
| --- | --- |
| `reviewd poll` | 1 回ポーリングして、条件に合うレビュー依頼で Claude Code を起動する（launchd が呼ぶ） |
| `reviewd poll --dry-run` | 検知とルール判定だけ。拾われない理由も表示する |
| `reviewd seed` | 現在のレビュー依頼を実行せず「処理済み」として記録する |
| `reviewd run --repo owner/name --pr 123` | 処理済みかどうかを無視して手動で 1 回実行する |
| `reviewd status` | 直近の実行履歴 |
| `reviewd rules validate` | 設定ファイルの検証と内容表示 |
| `reviewd daemon install / uninstall / status / plist` | launchd 登録の管理。`plist` は書き込まず内容だけ表示 |

## 設定

### rules.yaml

上から順に評価し、**最初にマッチしたルールだけ**が実行される。

```yaml
version: 1
rules:
  - name: review-requested
    on: [review_requested]
    match:
      repos: ["YasushiKobayashi/*"]
      exclude_authors: ["*[bot]"]
      exclude_labels: ["wip", "do-not-review"]
      include_draft: false
    action:
      prompt: |
        {{.Repo}}#{{.PRNumber}} をレビューして。
        差分は `git diff origin/{{.BaseRefName}}...HEAD`。
      timeout: 20m
      notify: true
```

`match.*` のパターン: `*` は `/` 以外の任意、`**` は `/` を含む任意、`?` は 1 文字。
大文字小文字は区別しない。`exclude_*` は対応する許可リストより常に優先される。

`action.prompt` は Go の `text/template`。使える変数は
`{{.Repo}}` `{{.PRNumber}}` `{{.PRTitle}}` `{{.PRURL}}` `{{.Author}}`
`{{.HeadRefName}}` `{{.HeadSHA}}` `{{.BaseRefName}}` `{{.IsDraft}}` `{{.Labels}}`。

#### `action.workdir`: 既存 clone で起動する（MCP 対策）

`action.workdir` を書くと、**reviewd は worktree を用意せず**、そのディレクトリで claude を
起動する（`workdir` も `text/template`）。

必要になる理由は Claude Code の設定が **セッションを開始したディレクトリに紐づく**ため:

- `claude mcp add` の既定は `local` スコープで、`~/.claude.json` に**パスをキーにして**保存される
  → reviewd が作る worktree からは設定済みの MCP サーバーが **1 本も見えない**
- `.mcp.json`（project スコープ）の承認も同じくパス単位
  → worktree のパスは PR ごとに新しいので、**毎回セットアップ確認が出る**

`user` スコープへ移せば解決するが、リポジトリ単位で MCP を管理したい場合は使えない。
その場合はこちらを使い、**隔離はプロンプト側で worktree を作らせて担保する**:

```yaml
action:
  mode: ghostty
  workdir: ~/go/src/github.com/owner/repo
  prompt: |
    まず、このリポジトリの作業ツリーとブランチには一切触らずに、
    レビュー専用の worktree を作ってそこへ移動してください。

    git fetch origin refs/pull/{{.PRNumber}}/head
    git worktree add --detach ../{{.Name}}-pr{{.PRNumber}} {{.HeadSHA}}
    cd ../{{.Name}}-pr{{.PRNumber}}

    （`git switch` / `git checkout` は使わないこと）

    移動できたら、そこでレビューしてください。
```

> **トレードオフ**: この方式では clone の `.git` に PR の ref と worktree の管理情報が
> 書き込まれる。「作業中のリポジトリを一切触らない」は **現在のブランチと作業ツリーには
> 触らない**まで緩む。`fetch` と `git worktree add` は HEAD も working tree も変えないので
> 実害はないが、完全な隔離ではない。

### config.yaml

`claude.extra_args`（headless 用）の既定は **読み取り専用**に閉じてある。

- `--allowedTools`: `Read` / `Grep` / `Glob` / `git diff|log|show|status|rev-parse`
- `--disallowedTools`: `Edit` / `Write` / `NotebookEdit` / `git push|commit` / `gh`

`--permission-mode plan` は使わない。plan モードは「実行せず計画を返す」挙動になり、
レビュー本文が返ってこないため。

一方 `claude.interactive_args`（ghostty 用）の既定は **空**。この締め付けは `claude -p` が
許可を尋ねられない（未許可ツールは黙って拒否される）ための措置であって、人がいる対話
セッションには要らない。むしろ `--disallowedTools` は実行時に覆せないので、ここで `Write` や
`gh` を塞ぐと「タスクファイルを残す」「PR を読む」タイプのプロンプトが壊れる。

### モードの固定（`claude.model`）

既定は `default`（Claude Code の "Default (recommended)"）で、**headless / ghostty の
どちらでも起動時に `--model default` を明示的に渡す**。

`--model` はコマンドラインの指定が `settings.json` の `model` や `ANTHROPIC_MODEL`
環境変数より優先される。無人で走る reviewd が、自分の見ていない設定に引きずられて
意図しないモデルで起動する事故を防ぐための固定。空文字にすると `--model` を渡さず、
周囲の設定に従う。

> **注意**: `default` は「安いモデル」という意味ではない。Default (recommended) が
> 何に解決されるかはアカウント設定次第で、Opus 系のこともある。実際にどのモデルで
> 走ったかは headless なら `meta.json` / `stream.jsonl` に残る。**コストを抑えたいなら
> `model: sonnet` のように明示的に指定する。**

## 実行モード

`claude.mode`（rules.yaml の `action.mode` でルール単位に上書き可）で 2 通り選べる。

| | `headless`（既定） | `ghostty` |
| --- | --- | --- |
| 起動するもの | `claude -p` を子プロセスとして | Ghostty のタブで対話モードの claude |
| reviewd の待ち方 | 終了まで待つ | タブを開いた瞬間に戻る |
| 結果 | `result.md` に保存 + 通知 | **タブの中。reviewd には戻らない**（読むのは人） |
| worktree | 実行後に掃除 | **残す**（タブがまだ使っているため） |
| status の表示 | `success` / `failed` / `timeout` | `launched` |

`ghostty` モードは「レビュー依頼を検知したら、その PR の worktree で claude が起動済みのタブが
勝手に生えている」状態を作るためのもの。そのまま人が対話を引き継げる。

対話セッションは **Remote Control 有効で起動する**（`claude.remote_control`、既定 `true`）。
セッション名は `reviewd-<owner>__<name>-pr<N>` が自動で付くので、別デバイスから
どの PR のレビューかを見分けて操作できる。`headless` では効かない（`claude -p` に
操作対象のセッションが無いため）。

```sh
reviewd run --repo owner/name --pr 123 --mode ghostty   # 1 件だけ試す
```

**注意**:

- Ghostty 1.3 以降が必要（AppleScript 辞書はそこで入った）。
- **呼び出し元プロセスに「Ghostty を制御」の自動化許可（TCC）が要る。** 初回は許可ダイアログが出る。
  launchd から起動した reviewd は別プロセス扱いなので改めて許可が必要で、しかも
  バックグラウンド起動だとダイアログが出ないまま `-1743 Not authorized` で失敗することがある。
  `daemon install` の後に **一度手で `reviewd run --mode ghostty` を実行して許可を取っておく**こと。
- worktree を掃除しないので `~/.reviewd/worktrees/` が溜まる。**掃除は自動化しない方針**なので、
  不要になったら `rm -rf ~/.reviewd/worktrees/<owner>__<name>/<name>-pr<N>` で手で消す
  （タブがまだ開いているものを消さないよう、消す前に `reviewd status` で確認する）。

## 動作の要点

- **作業中のリポジトリには一切触らない。** `~/.reviewd/repos/<owner>__<name>.git` に専用の
  bare mirror を作り、そこから detached worktree を生やす。
- **fork からの PR も動く。** head は `refs/pull/<N>/head` 経由で fetch する。
- **冪等キーは head SHA。** レビュー依頼が出ている間、新しい commit が積まれるたびに 1 回だけ走る。
  単なる poll の繰り返しでは再実行されない。
- **失敗しても再試行しない。** 起動を決めた時点で「処理済み」として記録する。失敗のたびに
  リトライしてトークンを溶かさないため。やり直しは `reviewd run` で明示的に行う。
- **多重起動しない。** `~/.reviewd/reviewd.lock` の flock。前の poll が走っていれば即 exit 0。
- **1 回の poll で起動するジョブ数に上限がある**（`poll.max_jobs_per_poll`、既定 3）。
  溢れた分は記録されないので次の poll で拾われる。

## 出力

```text
~/.reviewd/runs/<owner>__<name>/pr-<N>/<timestamp>/
  result.md     レビュー本文（通知のクリック先）
  stream.jsonl  claude の stream-json 生ログ
  stderr.log    claude の stderr
  meta.json     イベント / ルール / status / exit code / コスト / プロンプト
```

`ghostty` モードでは `prompt.md`（タブに渡したプロンプト）だけが残る。レビュー本文はタブの中にあり、
reviewd は回収しない。

## トラブルシュート

| 症状 | 確認すること |
| --- | --- |
| 期待した PR が拾われない | `reviewd poll --dry-run` の `no-rule` 行に理由が出る |
| launchd で動かない | `~/.reviewd/logs/reviewd.err.log`。`gh: command not found` なら `reviewd daemon install` をやり直す |
| private repo の clone に失敗する | `gh auth status`。mirror には `!gh auth git-credential` を焼き込んでいる |
| 実行が始まらない | `reviewd daemon status`。lock が残っていないか `~/.reviewd/reviewd.lock` |
| launchd 配下だけ `gh auth login` を要求される | `XDG_CONFIG_HOME` を使っているのに plist に入っていない。`reviewd daemon install` をやり直す |
| Ghostty タブが開かない | エラーに `-1743 Not authorized` が出ていれば TCC 未許可。手動で 1 回 `reviewd run --mode ghostty` を実行して許可する |
| Ghostty タブは開くが claude が起動しない | `keep_open: true` ならタブに理由が残っている。`claude: not found` なら plist の PATH が古い（`reviewd daemon install` をやり直す）|

## 開発

```sh
make pre-commit   # gofmt + go vet + go test
make test
```

`internal/worktree` は実 git に対する統合テストを持つ（ネットワーク不要。ローカル repo を
origin に見立てる）。`internal/runner` は shell script の fake claude を実プロセスとして起動し、
exit code・timeout・プロセスグループ kill・同時実行数を検証する。

## 現状のスコープ

検知しているイベントは `review_requested` のみ。以下は未実装（[doc/design.md](doc/design.md) §12）。

- `review_submitted` / 新規 commit / CI 完了 / Renovate 等の更新
- 結果の PR コメント投稿
- tmux セッションを立てて対話モードの claude を起動する出力先（Ghostty タブ版は実装済み）
- Slack 通知
