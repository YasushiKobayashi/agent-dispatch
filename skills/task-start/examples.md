# task-start: タスクファイルの具体例

`SKILL.md` の雛形（§2-c）は最小の骨組みだけなので、ここでは **実際に埋まった状態のゴールイメージ** を示す。
題材は reviewd の実際の開発で起きたこと。issue 番号・PR 番号はプレースホルダ（`#123` 等）。

共通の慣習（全例で守る）:

- **冒頭 2 行の指示文は必ず付ける**（運用ルール。消さない・書き換えない）。
- **タイトルは H1**。issue 駆動なら末尾に `(#123)` を付けるか、直下に Issue リンクを置く。
- **タイトル直下に `## 関連リンク` セクションを置く**（ページ最上部）。Issue / PR / 関連 issue・サブタスク / OpenSpec change / 設計 doc を集約する。**タスク中に PR や issue を新規作成したら、その URL をここに即追記**（後回しにしない）。
- **チェックリストは `## チェックリスト` 見出しの下**。フラット、または `### Phase X` でグループ化。**箇条書きのネストは避ける**（グループ分けは `###` 見出しで行う）。
- **`## メモ` の見出しはタスク種別に合わせて増減してよい**。`### 原因 / 修正 / 検証 / 意思決定の理由` を最小セットとし、必要に応じて `### 仕様` `### 実測` `### 設計判断ログ` `### 残課題` `### 参考` 等を足す。
- **メモには必ず `internal/pkg/file.go:42` の参照** と 1 行の理由を添える。コードを貼るならフェンスで囲み、前後に説明を付ける。
- **外部コマンドの出力は実測を貼る**（`gh` の JSON / `claude` の stream-json / AppleScript の返り値）。reviewd はこれらの形状に依存しているので、憶測で書いたメモは後で害になる。
- **`### 残課題` は常に置く**（無ければ「なし」と明記）。**`### 意思決定の理由` は却下案も書く**。
- **lint / test は `make pre-commit`**（= `gofmt -l -w .` + `go vet ./...` + `go test ./...`）。

> 以下の各例は、見やすさのため 4 連バッククォート ` ```` ` でタスクファイル全体を囲んでいる（中のコードは通常の 3 連バッククォート）。実ファイルにはこの外側の囲みは無く、中身だけが `task.*.md` に入る。

---

## 例 1: 不具合修正タスクの 3 段階

同一タスク（`task.gitignore-cmd-dir.md`）が「新規作成直後 → 進行中 → 完了直前」でどう育つかを示す。
題材は reviewd で実際に起きた事故: **`.gitignore` に `reviewd` と書いたせいで `cmd/reviewd/` がディレクトリごと除外され、`git status` にも出ないまま commit から消えかけた**。

> 各段階の差分に注目。チェックが `[ ]` → `[x]` になるのと **同時に** `## メモ` の対応見出しが埋まる。
> メモを後回しにして一気に書かない——これが中断耐性の肝（SKILL.md §3）。

### 段階 1: 新規作成直後（§2 でユーザー承認を得て Write した直後）

`### 原因` 以降は空。チェックリストは「仮」であることを承認済み。

```markdown
task.gitignore-cmd-dir.md を見てタスク進めて
タスクの実行におけるメモはこのファイルに行って、実装に進めるにあたって重要なドキュメントは適宜残してください。

# fix: cmd/reviewd/ が git の管理下に入らない

## 関連リンク

- Issue: https://github.com/YasushiKobayashi/agent-dispatch/issues/123
- PR: 未作成
- OpenSpec change: なし（.gitignore 1 行の修正なのでスキップ）
- 関連 issue・サブタスク: なし

## チェックリスト

- [ ] 再現条件の特定（どのファイルが git に見えていないか）
- [ ] なぜ git status に出ないのかを調査
- [ ] 原因の特定
- [ ] 修正
- [ ] 同じ穴が他の ignore パターンにも無いか確認
- [ ] make pre-commit 通過

---

## メモ

### 原因

### 修正

### 検証

### 意思決定の理由

### 残課題
```

### 段階 2: 進行中（原因特定済み・修正着手・検証未了）

`### 原因` が埋まり対応チェックが `[x]` に。`### 修正` は途中で、確定前の試行は「修正案:」として残す。
調査中に判明した「他の ignore パターンも見る」を、ネストではなく **チェックリスト本体に 1 行追記**。

````markdown
task.gitignore-cmd-dir.md を見てタスク進めて
タスクの実行におけるメモはこのファイルに行って、実装に進めるにあたって重要なドキュメントは適宜残してください。

# fix: cmd/reviewd/ が git の管理下に入らない

## 関連リンク

- Issue: https://github.com/YasushiKobayashi/agent-dispatch/issues/123
- PR: 未作成
- OpenSpec change: なし（.gitignore 1 行の修正なのでスキップ）
- 関連 issue・サブタスク: なし

## チェックリスト

- [x] 再現条件の特定（どのファイルが git に見えていないか）
- [x] なぜ git status に出ないのかを調査
- [x] 原因の特定
- [ ] 修正方針の確認（先頭 `/` でアンカーする）
- [ ] `.gitignore` の該当行を修正
- [ ] 同じ穴が他の ignore パターンにも無いか確認
- [ ] make pre-commit 通過

---

## メモ

### 原因

`git status --untracked-files=all` に `cmd/reviewd/main.go` が一切現れない。
`.gitignore:5` が次のようになっていた:

```gitignore
# build artifacts
/bin/
/dist/
reviewd        # ← これ
```

gitignore のパターンは **`/` を含まない場合、リポジトリ内の全階層のあらゆる深さに再帰的にマッチする**。
つまり `reviewd` はルート直下のビルド成果物だけでなく、**`cmd/reviewd/` ディレクトリにもマッチして
配下ごと除外していた**。ディレクトリ自体が ignore されると中身は走査されないため、
`--untracked-files=all` でも出てこない。これが「気づけない」理由の本体。

実測（修正前）:

```console
$ git check-ignore -v cmd/reviewd/main.go
.gitignore:5:reviewd	cmd/reviewd/main.go
```

### 修正

修正案: 先頭に `/` を付けてリポジトリルート直下にアンカーする。

```gitignore
# root-level binary only: a bare "reviewd" would also ignore cmd/reviewd/
/reviewd
```

（調査中）他のパターン（`*.db` 等）にも同種の取りこぼしが無いか確認する。

### 検証

（未着手）

### 意思決定の理由

`reviewd` を残したまま `!cmd/reviewd/` で negate する案も検討したが却下。
gitignore の negate は「親ディレクトリが ignore されていると効かない」という別の罠があり、
そもそもパターンが増えるほど読み解けなくなる。**先頭 `/` でアンカーする方が意図が 1 行で読める**。

### 残課題

- 他の ignore パターンの同種チェックは調査中。
````

### 段階 3: 完了直前（§6 着地確認をパスした状態）

全チェック `[x]`、最小 4 見出し充足、誤りと確定した試行錯誤は掃除済み
（`### 意思決定の理由` の却下根拠は残す）。`### 検証` には **修正前後で `git check-ignore` の結果が反転する** =
退行を検知できる証拠まで書く。この段階がそのまま SKILL.md §7 の PR description に転記できる。

````markdown
task.gitignore-cmd-dir.md を見てタスク進めて
タスクの実行におけるメモはこのファイルに行って、実装に進めるにあたって重要なドキュメントは適宜残してください。

# fix: cmd/reviewd/ が git の管理下に入らない

## 関連リンク

- Issue: https://github.com/YasushiKobayashi/agent-dispatch/issues/123
- PR: https://github.com/YasushiKobayashi/agent-dispatch/pull/125
- OpenSpec change: なし（.gitignore 1 行の修正なのでスキップ）
- 関連 issue・サブタスク: なし

## チェックリスト

- [x] 再現条件の特定（どのファイルが git に見えていないか）
- [x] なぜ git status に出ないのかを調査
- [x] 原因の特定
- [x] 修正方針の確認（先頭 `/` でアンカーする）
- [x] `.gitignore` の該当行を修正
- [x] 同じ穴が他の ignore パターンにも無いか確認 → `*.db` 等は意図通りなので変更なし
- [x] make pre-commit 通過

---

## メモ

### 原因

gitignore のパターンは **`/` を含まない場合、全階層に再帰的にマッチする**。
`.gitignore:5` の `reviewd`（ルート直下のビルド成果物を除外する意図）が
`cmd/reviewd/` **ディレクトリ**にもマッチし、配下ごと除外していた。
ディレクトリが ignore されると中身は走査されないため、`git status --untracked-files=all`
でも現れず、**commit から丸ごと欠落しても気づけない**状態だった。

### 修正

- `.gitignore:5` — `reviewd` → `/reviewd`。意図（ルート直下のバイナリのみ）をコメントで明示。

```gitignore
# root-level binary only: a bare "reviewd" would also ignore cmd/reviewd/
/reviewd
```

### 検証

- 修正前後で `git check-ignore -v cmd/reviewd/main.go` の結果が反転することを確認:

  ```console
  # 修正前
  $ git check-ignore -v cmd/reviewd/main.go
  .gitignore:5:reviewd	cmd/reviewd/main.go
  # 修正後（非ヒット = exit 1）
  $ git check-ignore -v cmd/reviewd/main.go; echo "exit=$?"
  exit=1
  ```

- `git status --untracked-files=all` に `cmd/reviewd/main.go` が現れることを確認。
- 他の ignore パターン（`/bin/` `/dist/` `*.db*` `.DS_Store`）を `git check-ignore -v` で
  1 つずつ確認。いずれも意図通りで、同種の巻き込みは無し。
- `make pre-commit` 通過。

### 意思決定の理由

`reviewd` を残して `!cmd/reviewd/` で negate する案は却下。negate は「親ディレクトリが
ignore されていると効かない」という別の罠を持ち込み、パターンが増えるほど読み解けなくなる。
**先頭 `/` でアンカーする方が意図が 1 行で読める**。

なお「ビルド成果物をルート直下に置かない（`bin/` のみにする）」案も検討したが、
`make build` の出力先は既に `bin/reviewd` であり、ルート直下の `/reviewd` は
`go build ./cmd/reviewd` を手で叩いたときの保険。消すと事故るので残す。

### 残課題

- なし
````

---

## 例 2: 機能開発 / 大きめタスク（見出しを増やす書き方）

機能開発や外部ツール連携を伴うタスクでは、最小 4 見出しだけでは足りない。
チェックリストを `### Phase X` でグループ化し、`## メモ` に `### 仕様` / `### 実測` /
`### 設計判断ログ` / `### 残課題` 等のドメイン見出しを足す。

題材は reviewd の **Ghostty 出力モード**（PR 検知 → Ghostty タブ生成 → worktree → 対話 claude 起動）。
以下は進行中スナップショットの形。

````markdown
task.ghostty-sink.md を見てタスク進めて
タスクの実行におけるメモはこのファイルに行って、実装に進めるにあたって重要なドキュメントは適宜残してください。

# feat: レビュー検知時に Ghostty タブで対話 claude を起動する (#456)

## 関連リンク

- Issue: https://github.com/YasushiKobayashi/agent-dispatch/issues/456
- PR: https://github.com/YasushiKobayashi/agent-dispatch/pull/460 (Draft)
- OpenSpec change: `openspec/changes/add-ghostty-run-mode/`
- 関連 issue・サブタスク: #461（`reviewd gc`。worktree が溜まる問題。今回スコープ外として分割）
- 設計 doc: [doc/design.md](doc/design.md) §runner

## チェックリスト

### Phase 0: 実現可能性の確認

- [x] Ghostty が AppleScript に対応しているか確認
- [x] タブ生成 + コマンド起動が可能か実機で検証
- [x] 出力（画面バッファ）を読めるか確認 → 読めない。仕様として受け入れる

### Phase 1: 設定スキーマ

- [x] `claude.mode` (headless | ghostty) を config に追加
- [x] `claude.interactive_args` を追加（headless 専用フラグを分離）
- [x] `ghostty` セクション（app_path / window / activate / keep_open）を追加
- [ ] `internal/cli/templates/config.yaml` の雛形にも反映

### Phase 2: 実装

- [ ] `internal/ghostty` パッケージ（osascript ラッパ）
- [ ] `internal/app` で mode 分岐
- [ ] ghostty モードでは worktree を残す（cleanup できないため）

### Phase 3: 検証

- [ ] fake osascript でのユニットテスト
- [ ] 実機で PR 1 件を ghostty モードで起動
- [ ] make pre-commit 通過

---

## メモ

### 仕様

- `claude.mode: ghostty` のとき、reviewd は **claude の終了を待たない**。タブを開いた時点で戻る。
- 結果の回収（`result.md` / `stream.jsonl`）は **headless モード限定**。ghostty モードでは
  ユーザーがタブを見て判断する。これは制約ではなく **意図した仕様**（ユーザー確認済み:
  「何を出力したかは最終的にユーザーがチェックすればいいから、初期起動だけできていればいい」）。
- `processed` の記録タイミングは従来通り「起動時点」。ghostty モードでも変えない。

### 実測

Ghostty 1.3.1 は AppleScript 辞書を持つ（`sdef /Applications/Ghostty.app` で確認）。
使うコマンドは次の 4 つ:

```applescript
tell application "Ghostty"
  set cfg to new surface configuration
  set command of cfg to "claude --allowedTools ..."
  set initial working directory of cfg to "<worktree>"
  set wait after command of cfg to true
  set t to new tab in window 1 with configuration cfg   -- ★ in <window> は必須
  input text "<prompt>" to (focused terminal of t)
  send key "enter" to (focused terminal of t)
end tell
```

**踏んだ罠**: `new tab` を `in <window>` 無しで呼ぶと `-1708 (Can't continue new tab)` で落ちる。
しかも **エラーになった時点でタブ自体は作られてコマンドも走っている**ため、
エラーを見て素朴に再試行すると **二重にタブが開く**。必ず `in window N` を指定する。

`terminal` クラスのプロパティは `id` / `name`（タイトル）/ `working directory` の 3 つだけ。
**スクリーンバッファを読む API は無い**。これが「出力を回収できない」の根拠。

### 修正

- `internal/config/config.go:53-65` — `ModeHeadless` / `ModeGhostty` と `AllModes` を追加。
- `internal/config/config.go:67-79` — `ClaudeConfig.Mode` / `.InteractiveArgs` を追加。
  `--output-format` `--verbose` は `-p` 専用なので interactive 側からは外す。
- `internal/config/config.go:81-94` — `GhosttyConfig`。`KeepOpen` は `*bool`
  （未指定 = true にしたいので、ゼロ値 false と区別する必要がある）。

### 設計判断ログ

| 決定                                          | 根拠                                                                                     |
| --------------------------------------------- | ---------------------------------------------------------------------------------------- |
| tmux ではなく Ghostty                         | ネイティブタブで人がそのまま操作を引き継げる。tmux は attach が要る                      |
| 出力回収を諦める                              | Ghostty に画面読み取り API が無い。ユーザーが「初期起動だけでよい」と明示                |
| `internal/runner` を再利用せず別パッケージに  | runner は「子プロセスを持って終了を待つ」前提。ghostty は起動して即戻るので前提が真逆    |
| ghostty モードでは worktree を残す            | claude の終了を検知できないので `defer tree.Remove()` が使えない。掃除は #461 の gc へ    |
| `KeepOpen` を `*bool` に                      | 未指定時のデフォルトを true にしたい。ゼロ値 false と「明示的な false」を区別する必要がある |

### 検証

（Phase 3 で着手予定。方針: `internal/ghostty` は osascript 実行を `execx.Runner` 差し替えで
fake し、生成される AppleScript 文字列をアサートする。`internal/notify` の
AppleScript エスケープテストと同じ方式——**文字数カウントではなく構文レベルで検証**する。）

### 意思決定の理由

上記「設計判断ログ」の通り。特に **runner を再利用しなかった判断**が中心。
`internal/runner/runner.go:143` の `exec.CommandContext` は timeout / プロセスグループ kill /
semaphore による同時実行制御を持つが、これらはすべて「reviewd が claude の親プロセスである」
ことが前提。ghostty モードでは claude の親は Ghostty なので、この機構は 1 つも使えない。
無理に共通化すると両方が壊れるため、`Sink` として並列に置く。

### 残課題

- [ ] **TCC（自動化許可）の扱い**: launchd から起動した reviewd は別プロセス扱いなので、
      `osascript` → Ghostty の自動化許可が改めて必要。バックグラウンド起動だと許可ダイアログが
      出ないまま `-1743 (Not authorized)` で失敗する可能性が高い。
      → `daemon install` 後に 1 回手動で `reviewd poll` を回して許可を取る手順を README に書く。
- [ ] worktree が溜まる問題は #461（`reviewd gc`）で追跡。

### 参考

- `internal/app/app.go:362-406` — worktree 用意 → mode 分岐 → runner 実行の流れ。`app.go:369` の
  `keepTree := r.Action.KeepWorktree || mode == config.ModeGhostty` が「ghostty では掃除しない」の実装。
- `internal/notify/notify.go:95` — 既存の osascript 呼び出し。AppleScript エスケープの実装が流用できる。
- `internal/runner/runner.go:143` — headless の claude 起動。ghostty 版と対比して読むと差が分かる。
````

> ポイント: **見出しは固定ではない**。バグ修正は `原因/修正/検証/意思決定` が主軸、
> 外部ツール連携は `仕様/実測/設計判断ログ` を足す——タスク種別に合わせて器を作る。
> 固定なのは「冒頭 2 行の指示文」と「`file:line` で引用する」「外部コマンドは実測を貼る」原則だけ。

---

## 補足: 既存の `task.auto.md` について

リポジトリ直下の `task.auto.md` は reviewd の MVP 全体（設計 → 14 サブタスク → 実機検証 → README）を
1 ファイルで回した実物で、**例 2 の「完了直前」に相当する状態**になっている。実際にどこまで書くかの
分量感が知りたければこれを読むのが早い。特に参考になるのは:

- `### 意思決定の理由` に **却下した選択肢とその理由**（GitHub Actions 案 / Shell 実装案 / PR 自動コメント案）が
  すべて残っていること。「なぜこの形なのか」が後から辿れる。
- `### 修正` が **ファイル 1 つにつき 1-3 行** で、何を新規作成し何を変えたかが漏れなく並んでいること。
- `### 検証` に **実測値**（bare clone 14s / 164MB、claude 9s success、$0.59）が入っていること。

ただしファイル名は汎用名 `task.auto.md` で、SKILL.md の命名規約からは外れる（歴史的経緯）。
**新規作成では真似しない**。
