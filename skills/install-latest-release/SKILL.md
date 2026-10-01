---
name: install-latest-release
description: reviewd の最新（または指定 tag の）GitHub Release から darwin バイナリを取得し、checksum を検証して ~/.local/bin/reviewd に展開する。「最新のバイナリ展開して」「reviewd 更新して」「リリース版入れて」と言われた時に使用。
argument-hint: '[tag（省略時は Latest。例 v0.1.0）]'
allowed-tools: Bash(gh:*), Bash(shasum:*), Bash(tar:*), Bash(install:*), Bash(uname:*), Bash(mkdir:*), Bash(rm:*), Bash(ls:*), Bash(git:*), Bash(~/.local/bin/reviewd:*), Bash(reviewd:*)
---

# reviewd の最新リリースを展開する

main への push でテストが通ると CI（`.github/workflows/ci.yml` の `release` job）が tag を切り、
goreleaser で `reviewd_<ver>_darwin_{arm64,amd64}.tar.gz` と `checksums.txt` を Release に上げる。
取得は `gh` を使う（tag 解決と checksums.txt の取得をまとめられる）。

## 手順

1. **対象 release を確認する**
   - `gh release list --repo YasushiKobayashi/agent-dispatch -L 3`
   - 引数で tag が渡されていればそれを、無ければ Latest を使う。
   - 直近の main push の CI がまだ走っている場合は `gh run list --repo YasushiKobayashi/agent-dispatch --branch main -L 3` で状況を見て、
     未完了ならユーザーに「待つか、今の Latest を入れるか」を伝える。
   - 現在のバージョンを `~/.local/bin/reviewd --version` で確認し、同じなら「既に最新」と報告して終了してよい。

2. **ダウンロードと checksum 検証**（作業ディレクトリは scratchpad。無ければ `mktemp -d`）
   ```sh
   ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
   D=<scratchpad>/reviewd-release; rm -rf "$D"; mkdir -p "$D"
   gh release download [<tag>] --repo YasushiKobayashi/agent-dispatch \
     --pattern "*darwin_${ARCH}.tar.gz" --pattern checksums.txt --dir "$D"
   (cd "$D" && shasum -a 256 -c --ignore-missing checksums.txt)
   ```
   checksum が `OK` にならなければ **インストールせず中断** して報告する。

3. **展開とインストール**
   ```sh
   tar -xzf "$D"/*darwin_${ARCH}.tar.gz -C "$D" reviewd
   install -m 0755 "$D/reviewd" ~/.local/bin/reviewd
   ~/.local/bin/reviewd --version
   ```
   `--version` が対象 tag（`v` 抜き）と一致することを確認する。`dev` のままなら失敗扱い。

4. **daemon の確認**
   - `~/.local/bin/reviewd daemon status` で `program` が `~/.local/bin/reviewd` を指していることを確認する。
   - launchd は `StartInterval` で毎回バイナリを起動し直すので、**再起動や `daemon install` のやり直しは不要**。
     ただし新リリースで plist の内容（PATH や引数）に変更が入っている場合は `reviewd daemon plist` の出力と現行 plist を比べ、
     差分があればユーザーに `daemon install` の再実行を提案する（勝手に実行しない）。

## 報告

旧バージョン → 新バージョン、checksum 検証結果、daemon の状態を 2〜3 行で伝える。
