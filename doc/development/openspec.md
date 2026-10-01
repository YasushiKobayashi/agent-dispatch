# OpenSpec 運用ガイド（開発ライフサイクル / doc との使い分け）

reviewd では [OpenSpec](https://github.com/Fission-AI/OpenSpec) を「ある変更(Change)」の背景・検討・意思決定を残す仕組みとして導入している。本ドキュメントで **OpenSpec の運用ルールを定義する**（運用に関する取り決めはここに集約する）。

- 設計の SSOT: [../design.md](../design.md)
- 導入手順: [../../README.md](../../README.md)
- 個人の作業ログ運用（task-start）: `skills/task-start/SKILL.md`

## セットアップ

OpenSpec CLI はグローバルにインストールする。

```bash
npm install -g @fission-ai/openspec@latest   # bun 派なら bun add -g @fission-ai/openspec@latest
openspec --version                           # 1.5.0 以上
```

リポジトリ側の初期化は済んでいる（`openspec init --tools claude` 実行済み）。生成物:

- `openspec/config.yaml` — プロジェクト context と artifact ルール（AI が artifact 生成時に読む）。**ここは手で書く**
- `openspec/changes/<change>/` — 進行中の変更（proposal / design / tasks / specs）
- `openspec/changes/archive/` — 完了してアーカイブした変更
- `openspec/specs/<capability>/spec.md` — 現行仕様のベースライン（delta spec の統合先。delta spec は後述）
- `skills/openspec-*/SKILL.md`（`.claude/skills` → `../skills` の symlink 経由で参照）— ワークフロー用 skill 5 本（propose / apply-change / archive-change / explore / sync-specs）。**`openspec init` が生成する英文ファイル**（upstream: [Fission-AI/OpenSpec](https://github.com/Fission-AI/OpenSpec)、MIT ライセンス。frontmatter の `license` / `metadata.generatedBy` 参照）。`openspec update` で上書き更新されるため、**手編集・和訳はしない**

instruction files の更新は `openspec update` で行う。

## 開発ライフサイクル

実装前の認識合わせと、変更時点の意思決定の記録を優先する。

```text
Issue → OpenSpec(proposal / tasks) → task-start → 実装 → Archive（完了 change・同一 PR）→ Review → マージ → doc更新
```

| Phase         | 内容                                                                                          | 使うもの                                     |
| ------------- | --------------------------------------------------------------------------------------------- | -------------------------------------------- |
| 1. Issue      | 背景・要望の起点（精度は低くてよい）                                                          | GitHub Issue                                 |
| 2. OpenSpec   | 実装前の認識合わせ。**proposal.md / tasks.md 必須**、design.md / specs は任意                | `openspec new change` / `openspec-propose`   |
| 3. task-start | 個人の作業ログ・ローカル TODO 管理                                                            | `task.*.md` / `/task-start`                  |
| 4. 実装       | tasks.md の受け入れ条件に沿って実装。`make pre-commit` を通す                                 | Go の規約 / Makefile                         |
| 5. Archive    | **完了した change を実装と同じ PR 内で** `openspec/changes/archive/` へ。delta を specs に統合 | `openspec archive <change>`                  |
| 6. Review     | コード + OpenSpec（archive 移動含む）+ doc の整合確認                                         | PR レビュー                                  |
| 7. マージ     | archive 済みの状態で PR をマージ                                                              | GitHub PR                                    |
| 8. doc更新    | 確定した最新仕様を `doc/` に反映（主に `doc/design.md`）                                      | 手動                                         |

> **順序が要点**: archive は「マージ後の別作業」ではなく **実装と同じ PR に含める**（＝マージ前に済ませる）。詳細は次節。

### アーカイブの基本方針：完了 change は PR マージ前にアーカイブする

**目的はアーカイブ漏れの防止**。実装が終わった change をアーカイブせず放置すると、`openspec/changes/` に「実装済みだが未 archive」の change が溜まる。PR がマージされた瞬間に archive の動機が切れて誰も戻ってこないのが構造的な原因。そこで **archive をマージ後の任意作業にせず、実装 PR の一部にする**。

基本ルール:

- **完了した change は、その実装を含む PR の中で `openspec archive <change>` を実行してからマージする**。archive による移動（`changes/<name>/` → `changes/archive/<date>-<name>/`）と delta の specs 統合を **同じ PR の diff に載せて** レビューを受ける。
- tasks.md の受け入れ条件（`- [ ]`）がすべて満たされたら「完了」。archive 自体を「マージ後にユーザー判断で実施」といった **後回しの task にしない**（この書き方が漏れの原因になる）。archive はマージ前の必須ステップ。
- archive コマンド:
  - delta spec を持つ change: `openspec archive <change> -y`（specs/ をベースラインに統合）
  - tooling / doc のみで delta spec が無い change: `openspec archive <change> -y --skip-specs`

**例外：複数フェーズの change はすべてを archive しなくてよい**

- 1 つの change が複数フェーズに分かれ、**一部フェーズだけ完了して先に PR を出す**ことがある。この場合、change 全体が完了していないなら **archive しないまま残してよい**（＝「完了フェーズだけ archive」する必要はない。change 単位でしか archive できないため）。
- フェーズごとに独立して archive したいなら、**change を分割**する。分割しないなら、全フェーズ完了時にまとめて archive する。
- 判断基準: 「この PR で change の tasks がすべて `[x]` になるか？」が Yes なら archive してマージ、No（フェーズ継続）なら残す。

### artifact の必須／任意

| artifact                            | 位置づけ | 備考                                                 |
| ----------------------------------- | -------- | ---------------------------------------------------- |
| `proposal.md`                       | **必須** | Why（なぜ）/ What Changes（何を）。How は書かない    |
| `tasks.md`                          | **必須** | 実行可能な粒度のチェックリスト = 受け入れ条件        |
| `design.md`                         | 任意     | 非自明な設計判断・代替案・トレードオフがあるときだけ |
| `specs/<cap>/spec.md`（delta spec） | 任意     | spec-level の振る舞い/制約が変わるときだけ           |

> **delta spec**（OpenSpec 用語）: 現行仕様のベースライン（`openspec/specs/<capability>/spec.md`）に対する **差分だけ** を記述した spec。change の `specs/` 配下に置き、archive 時にベースラインへ統合される。一般に流通した用語ではないため、本ドキュメント外で使うときは説明を添える。

**reviewd における「spec-level」の目安**: 検知するイベント種別、`rules.yaml` / `config.yaml` のスキーマ、CLI サブコマンドの契約、冪等性の保証。これらが変わるなら delta spec を書く。内部リファクタ・ログ文言・テスト追加は spec ではない。

### エスケープハッチ: 小さい変更は OpenSpec をスキップしてよい

バグ修正・軽微な文言調整・テスト追加など、実装前の認識合わせが薄い変更は change を作らず、従来通り task-start（`task.*.md`）だけで進めてよい。スキップ判断は担当者/AI に委ねる。

### タスクの内容が変わったら開発サイクルを見直す（増減・方針転換すべて）

ライフサイクル図は「着手時に一度 OpenSpec を作る」線形フローに見えるが、**実際には再突入がある**。長いタスクほど、実装の途中で **タスクが「何をするものか」自体が変わる**。これは「スコープが増える」だけではない:

- **追加**: 当初スコープに無かった substantive な変更が生える
- **削除**: 提案していた capability を落とす・別 PR に切り出して本 change から外す
- **方針転換**: やり方を変える（例: 結果の出力先を headless のファイル保存から、対話セッションの起動へ切り替える）

いずれの場合も、放置すると **proposal の What Changes / tasks が実態とズレる**。OpenSpec（proposal/tasks）は変更の SSOT なので、**実装が進むにつれて常に「今のタスク定義」と一致していなければならない**。

そこで次を運用ルールとする:

- **タスクの内容が変わったら開発サイクルを最初から見直す**: スコープの **追加・削除・方針転換のいずれでも**、「何をするものか」が変わったら、その時点で**実装を止めて** Issue → proposal → tasks を照らし直す。「増えた分を追記する」のではなく、**変わった後の姿に proposal の What Changes / tasks を書き直す**（不要になった項目の削除・置き換えも含む）。当初 change と独立した substantive 変更なら別 change を作る。
- **commit 前チェックポイント**: commit する前に「この commit 群を含め、現在の proposal / tasks は今のタスク定義と一致しているか」を一度自問する。ズレていれば commit 前に更新する（§エスケープハッチ相当の小変更なら不要）。
- **判断基準はエスケープハッチと同じ**: バグ修正・typo・軽微な調整は従来どおり skip してよい。「新しい外部契約（gh / claude / launchd との境界、config・rules スキーマ）/ 新しいイベント種別 / 意思決定を伴う設計変更・方針転換」が入ったら skip しない。

> この再突入チェックは `git commit` を検知する PostToolUse hook 等で機械化できるが、**reviewd には未導入**。ここでは上記の運用ルール（人 / AI の自問）が唯一の経路なので、commit 前チェックポイントを省かないこと。

### 注意: `openspec validate` は最低 1 つの delta を要求する

`openspec validate <change>`（strict でなくても）は change に最低 1 つの delta spec を要求する。したがって:

- **spec を省いた change は `openspec validate` を通らない**。artifact の進捗は `openspec status --change <change>` で確認する。
- spec を書く change なら `openspec validate --strict <change>` まで通す。

## OpenSpec と doc の責務分担

|          | `doc/`（現在の期待仕様）                                 | OpenSpec（Working Document）                     |
| -------- | -------------------------------------------------------- | ------------------------------------------------ |
| スコープ | システム全体                                             | 変更(Change)単位                                 |
| 内容     | 最新仕様・アーキテクチャ・運用ルール・設定スキーマ・決定事項 | 変更の背景・要件・検討・設計・変更時点の意思決定 |
| 時制     | 現在の期待仕様（結果）                                   | 変更時点（過程）                                 |
| 目的     | 「今どうなっているか」を理解する                         | 実装前の認識合わせ・判断材料・変更履歴の保存     |

reviewd の `doc/` の中身:

- `doc/design.md` — 全体構成 / ディスクレイアウト / config・rules スキーマ / イベントモデル / 冪等性 / CLI / launchd。**設計の SSOT**
- `doc/development/openspec.md` — 本ドキュメント（開発ライフサイクルの運用ルール）

### doc に書くもの / OpenSpec に書くもの

**doc に書く**: 最新仕様、アーキテクチャ、運用ルール、設定スキーマ、確定した決定事項。doc の理解に必要な範囲の背景は書いてよい。

**OpenSpec に書く（doc に転記しない）**:

- 検討した代替案
- 実装時の判断理由
- スコープ外とした内容
- レビュー時の議論
- 実装時のみ必要な背景

位置づけとしては、Spec Driven Development を前提に ADR（Architecture Decision Record: 意思決定記録）を変更(Change)単位へ拡張したものに近い。

doc から検討経緯を辿らせたいときは、過程を転記せず OpenSpec への参照リンクを置く。

```md
詳細な検討経緯は openspec/changes/add-ghostty-sink/ を参照してください。
```

## task-start との連携

既存の task 管理（個人タスク・ローカル TODO）は継続する。役割を混ぜないこと。

- **OpenSpec（`tasks.md`）**: その Change の受け入れ条件。変更の契約。commit 対象。
- **task-start（`task.*.md`）**: 個人の作業ログ・試行錯誤・現在地メモ。gitignore 対象で commit されない。

`/task-start` は OpenSpec change を起点に、`openspec/changes/<change>/` の proposal / tasks を読み込んで実装開始状態を作る使い方ができる。詳細は `skills/task-start/SKILL.md`。

## Review 時の整合性確認

PR レビュー時に以下の整合を確認する。

- OpenSpec に記載された変更内容と実装が一致しているか
- 完了した change が同一 PR 内で archive されているか
- 関連する `doc/design.md` が更新されているか（設定スキーマ・CLI・イベント種別を変えた場合は必須）

## よく使うコマンド

```bash
openspec new change <name>                      # change 作成
openspec status --change <name>                 # artifact 進捗
openspec instructions proposal --change <name>  # artifact 作成手順（AI 向け）
openspec list                                   # 進行中 change 一覧
openspec validate <name> [--strict]             # 妥当性検証（要 delta）
openspec show <name>                            # 内容表示
openspec archive <name>                         # 完了 change をアーカイブ・specs 統合
openspec update                                 # instruction / skill を最新化
openspec doctor                                 # root 解決の健全性チェック
```

Claude Code からは `openspec-propose` / `openspec-apply-change` / `openspec-archive-change` / `openspec-explore` / `openspec-sync-specs` の各 skill が使える（`openspec init` 生成）。
