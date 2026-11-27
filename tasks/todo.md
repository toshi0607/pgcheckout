# pgcheckout 計画

設計は `DESIGN.md`（英語が正、日本語は `DESIGN.ja.md`）。検証コマンドはリポジトリのルートで実行する。

## Plan

- [x] S1 中核: 設定、名前、目印、方式の選択、`ensure`（直接複製）、`list`、`dsn`、`drop`、`reset`、`base create/adopt/open/close`、`doctor`
  - 完了条件: `go build ./... && go vet ./... && go test ./...` が exit 0。`PGCHECKOUT_TEST_ADMIN_URL=... go test -tags integration ./...` が PostgreSQL 18 で exit 0
  - 証跡（2026-11-22）: 単体 7 パッケージ ok。結合は PostgreSQL 18.7 で 28 件 PASS、exit 0
- [x] S2 プール: `pool fill/status/drain`、`ensure` のプール利用と切り離した補充、`gc`、`--ttl`
  - 完了条件: S1 と同じコマンドが exit 0。`pgcheckout ensure | cat` が補充の完了を待たずに戻る
  - 証跡: 1 GB のベースで `ensure | cat` が 41 ミリ秒で戻り、2 秒後に `pool status` の ready が 2
- [x] S3 git 連携: `hook install`、`ensure --env-file`
  - 完了条件: 一時リポジトリで `git checkout` 後に env ファイルの接続先が切り替わる
  - 証跡: 一時リポジトリで 3 回切り替え、`.env.pgcheckout`（当時の名前は `.env.pgtwig`）の接続先が毎回変わった。`~/.git-hooks` の更新時刻は変わらず
- [x] S4 AlloyDB Omni 17 で結合テスト
  - 完了条件: Omni 17 を指す `PGCHECKOUT_TEST_ADMIN_URL` で `go test -tags integration ./...` が exit 0
  - 証跡: AlloyDB Omni 17.10 で 3 回連続 exit 0。`TestDropInUseNeedsForce` は `-count=10` で exit 0
- [x] S5 レビュー（`reviewer` に DESIGN.md との照合を依頼）と指摘の反映
- [x] S6 README と AGENTS.md（エージェント向けの使い方）
- [x] S7 MCP サーバー（`pgcheckout mcp`、標準入出力、ツール 8 つ）
  - 完了条件: `go build ./... && go vet ./... && go vet -tags integration ./... && go test ./...` が exit 0。SDK のクライアントから全ツールを呼ぶ結合テストが PostgreSQL 18 と AlloyDB Omni 17 で exit 0。`initialize` と `server/discover` の両方の手順で `tools/list` が 8 件を返す。約 1 GB のベースで `ensure` の複製中に標準入力を閉じても、目印の無いデータベースが残らない
  - 証跡（2026-11-23）: 4 つのコマンドが exit 0。`go test -race ./internal/mcpserver` も exit 0。`internal/branch` と `internal/mcpserver` の結合テストが PostgreSQL 18.7 と AlloyDB Omni 17.10 で exit 0。実バイナリで、`initialize` と `server/discover` の両方の手順が 8 件を返した。約 1 GB のベース（`wal_log`）で `ensure` の複製中に標準入力を閉じると exit 0 で終わり、`tmp` は残らなかった。`notifications/cancelled` は `canceled` のツールの失敗を返し、サーバーは次の呼び出しに応えた。パスワード付きの接続先を渡しても、標準出力にパスワードは現れなかった

## 再開するときの続き（2026-11-22 に中断）

- [x] 修正後の差分を `reviewer` に再レビューさせた。重大な指摘は無し。軽微な指摘 7 件と細かな指摘 2 件を反映した（Review 節）
  - 証拠: `go build`、`go vet`、`go vet -tags integration`、`go test ./...`、`gofmt -l .` が exit 0。結合テスト 26 件が PostgreSQL 18.7 と AlloyDB Omni 17.10 で 2 回ずつ exit 0。実行後に取り残したデータベースは無し
- [x] README のフックの説明と `go install` の置き場所を直した（コミット 75da718）
- [x] MCP サーバー（S7 として実施）
- [x] GitHub への公開（2026-11-25 にトシの OK。公開リポジトリ `toshi0607/pgcheckout`）
- [x] CI と Dependabot（2026-11-25、PR #1）
  - 完了条件: PR の 4 つのチェックが成功する。`gh actions-lock --verify` が exit 0
  - 証跡: `build / vet / test`、`integration (PostgreSQL 18)`、`integration (AlloyDB Omni 17)`、`verify` がすべて成功。結合テストは PostgreSQL 18.7 と AlloyDB Omni 17.10 の Linux（x86_64）で `ok`。Linux での実行はこれが初めてである。マージコミットは 4f8cf09
- [x] 文書を英語を正にする（2026-11-25）
  - 完了条件: 原文と訳文で、コードブロック、バッククォートの中身、見出し、表の行数、箇条書きの数、リンクが一致する
  - 証跡: 3 組とも一致した。差は、訳した引数の名前（`[ブランチ]` を `[branch]` にしたものなど 14 件）、言語の切り替えリンク、英単語にした 1 桁の数だけである
- [x] 実クライアントから MCP サーバーを呼ぶ（2026-11-25）
  - 証跡: Claude Code 2.1.316 を `--mcp-config` と `--strict-mcp-config` で起動した。サーバーは `connected` になり、ツール 8 件が見えた。`doctor`、`ensure`（`feature/mcp-check`）、`list` がどれも失敗なしで返り、`ensure` は `source: "copy"`、57 ミリ秒だった。PostgreSQL 18.7 の側でも、ブランチ 1 件とプール 2 件の目印を確かめた。クライアントの設定には何も残していない

結合テストの環境は片付けた。再開するときは、次の 2 つを用意する。

- PostgreSQL 18: `/opt/homebrew/opt/postgresql@18/bin` の `initdb` と `pg_ctl` でクラスタを作り、`CREATEDB` と `CREATEROLE` を持つロールと、そのロールが所有する管理用データベースを作る。`LC_ALL=en_US.UTF-8` を付けないと起動に失敗する
- AlloyDB Omni 17: `docker run -d -e POSTGRES_PASSWORD=... -p 127.0.0.1:54330:5432 -v <volume>:/var/lib/postgresql/data google/alloydbomni:17`。ボリュームは `/var/lib/postgresql/data` に付ける

## Constraints

| Constraint | Source | Verify by |
|------------|--------|-----------|
| 対象は PostgreSQL 互換だけ。MySQL は扱わない | user msg（2026-11-22） | DESIGN.md「対象にしないもの」 |
| ローカル（PostgreSQL、AlloyDB Omni）を最初の対象にする | user msg | S1、S4 の結合テスト |
| db-branch と実装が同じである必要はない。保守しやすく、速く、エージェントが使いやすいこと | user msg | 依存は pgx と MCP の公式 SDK、JSON 既定、冪等な ensure、対話なし |
| 破壊的な SQL は目印のあるデータベースだけに実行する | advisor / DESIGN.md | 結合テスト `TestDropRefusesUnmanaged` など |
| GitHub への作成とプッシュはトシの OK が要る（2026-11-25 に OK が出て公開した） | rules（外へ出す操作） | `git remote -v` が `github.com/toshi0607/pgcheckout` |
| 「GCP」と書かず Google Cloud と書く | memory | grep |
| Codex は PostgreSQL に届かない。結合テストはメインのセッションで実行する | memory（codex-sandbox-go-recipe） | 実行ログ |

## Assumptions

| Assumption | Status | Evidence |
|------------|--------|----------|
| `file_copy_method` は一般ロールがセッションで設定できる | VERIFIED | PG 18.7: `pg_settings.context = user`、CREATEDB だけのロールで `SET` 成功 |
| 接続を禁止したベースを複製元にでき、複製先は接続できる | VERIFIED | PG 18.7 の探り（`datallowconn` が base=f、copy=t） |
| 複製はデータベース単位の設定、権限、コメントを写さない | VERIFIED | PG 18.7 の探り（`pg_db_role_setting`、`datacl`、`shobj_description`） |
| 名前の変更とコメントの書き換えは 1 つのトランザクションに入る | VERIFIED | PG 18.7 の探り（commit と rollback の両方） |
| 保護したベースの複製、複製で落ちる 3 項目、名前変更とコメントの原子性は AlloyDB Omni 17 でも同じ | VERIFIED | Omni 17.10 で同じ探りを CREATEDB だけのロールで実行（2026-11-22）。`file_copy_method` は 17 に無く、`unrecognized configuration parameter` になる |
| マネージドの AlloyDB で `clone` が速い | VERIFIED（成り立たない、2026-11-28） | 無料トライアルのクラスタ（AlloyDB 18.4、8 vCPU、東京）で計測した。`file_copy_method` に指定できる値は `copy` だけで、`clone` は使えない。1,029 MB の複製は、サーバー側の `CREATE DATABASE` で 2.3〜2.5 秒（`FILE_COPY` と `WAL_LOG` のどちらも）、pgcheckout 経由で 2.1〜3.4 秒。`WAL_LOG` の 1 回目だけ 34.7 秒かかった。名前の変更は 15 ミリ秒。手元からパブリック IP 経由でプールから割り当てると、全体で 0.15〜0.28 秒 |
| AlloyDB Omni の Docker イメージに PostgreSQL 18 は無い | VERIFIED | Docker Hub のタグ一覧（最新は 17.10、2026-11-22 時点） |
| 2 つの `ALTER DATABASE ... RENAME` と 2 つの `COMMENT ON DATABASE` は、1 つのトランザクションでまとめてコミットまたはロールバックされる | VERIFIED | PG 18.7 の探り（2026-11-25）。CI の結合テストで AlloyDB Omni 17.10 でも同じ |
| 接続が在るデータベースの名前変更は、約 5 秒待ってから `55006` で失敗する。別ロールの接続は `42501` で切れない | VERIFIED | PG 18.7 の探り（2026-11-25、5.1 秒） |
| 閉じたベースは `pg_dump -C` でダンプできない。開いたベースのダンプは、データベースのコメントを含む | VERIFIED | PG 18.7 の探り（2026-11-25） |
| MCP の公式 Go SDK v1.8.0 は、標準入出力で `initialize` と `server/discover` の両方に応える | VERIFIED | 2026-11-23 の試作: 両方の手順で `tools/call` が `structuredContent` を返した |
| 標準入力が閉じたとき、実行中のツールは中断され、後始末を待ってから終了する | VERIFIED | 2026-11-23 の実機確認: 複製中に標準入力を閉じて exit 0、`tmp` は残らず。サーバー側でも文脈を中断して待つ |

## 判断の記録

- 2026-11-25 目印が付いたまま戻るベースは、世代の規則だけで扱う。目印に OID を入れる案は、今回も採らなかった。閉じたベースはダンプできず、開いたベースの復元は `base close` を必ず通るので、規則で足りるためである。
- 2026-11-25 「世代がベースと違う複製は古い」と定義を変えた。以前は、世代が小さい複製だけを古いとしていた。世代がベースより大きいプールは、`gc` が消すようになる。
- 2026-11-25 `reset` で入れ替えた古いデータベースの目印は、世代 0 の `tmp` にした。レビューの指摘（古い世代のままだと `gc` が 1 時間待つ）を受けたものである。削除はロックの外で、1 分の期限で行う。
- 2026-11-25 レビューの指摘のうち、次の 2 つは直していない。`--force` が切るのはクライアントの接続だけで、拡張のバックグラウンドワーカーなどが接続していると `reset --force` は失敗する（以前は `DROP DATABASE ... WITH (FORCE)` で通っていた）。入れ替えの再試行の間、最長で約 77 秒ロックを持つ。どちらも、ブランチ用のデータベースでは起きにくいと判断した。
- 2026-11-25 レビュー後の修正（約 40 行）は、Codex に渡さずメインのセッションで行った。
- 2026-11-25 文書は英語を正にする。`README.md`、`DESIGN.md`、`spec/STATE_MODEL.md` を英語にし、日本語の原文は `*.ja.md` として残して、各ページの先頭で相互にリンクする。`AGENTS.md` は元から英語である。`tasks/` は作業の記録なので日本語のままにする。内容を変えるときは、英語と日本語の両方を直す。
- 2026-11-25 CI の結合テストは、PostgreSQL 18 と AlloyDB Omni 17 のサービスコンテナで実行する。Dependabot の更新は週末に CI の結果だけでマージされるので、単体テストだけでは pgx のメジャー更新を検査できないためである。AlloyDB Omni のジョブは `continue-on-error` にした。GitHub のランナーでの実績が 1 回しか無いためである。
- 2026-11-25 Dependabot は `gomod` と `github-actions` を週次にした。TLC は CI で実行しない。最大の設定が約 8,481 万状態を調べるためである。
- 2026-11-25 ワークフローの YAML は Codex に渡さず、メインのセッションで書いた。Codex のサンドボックスはネットワークに届かず、タグの解決と `gh actions-lock` を実行できないためである。
- 2026-11-25 公開にあたり、ライセンスは MIT にした。トシの Go の公開リポジトリが MIT でそろっているためである。`tasks/` は履歴ごと公開する。`casrec` が同じ扱いである。公開前に、履歴全体を betterleaks で調べて検出が無いこと、追跡ファイルに手元のパスが無いことを確かめた。CI は足していない。
- 2026-11-25 MCP サーバーは、どのクライアントの設定にも常設で登録しない。接続先とベースはプロジェクトごとに違い、いま常時動いている開発用のサーバーも無いためである。実クライアントからの呼び出しは、一時的な登録で確かめる。
- 2026-11-23 再レビューの指摘は、頻度と被害で d1〜d3 だけを直した。d3 は削除を足さず、名前と消し方を失敗に入れるだけにした。付け直しは名前だけで対象を決めるので（d4）、その場で削除まで進めると被害が大きくなるためである。
- 2026-11-23 MCP サーバーは公式の Go SDK v1.8.0 で作る。MCP は 2026-07-28 版で `initialize` を廃止しており、新旧の手順を自前で追うより SDK に任せるほうが保守しやすい。依存が pgx だけではなくなる。
- 2026-11-23 MCP の結果にはパスワードを含めない。ツールの結果は会話の記録に残るためである。CLI の出力は従来どおりパスワードを含む。
- 2026-11-23 MCP のツールにベースの操作、`pool drain`、`pool fill`、`hook` を入れない。ほかの利用者に影響する操作と、エージェントが呼ぶ必要の薄い操作である。
- 2026-11-22 名前は仮に `pgtwig` とした。GitHub のリポジトリ検索で同名が無かった。
- 2026-11-22 トシの決定で名前を `pgcheckout` に変えた。`pgbranch` など branch を含む素直な名前は既存のリポジトリと重なる。エージェントが打つ前提なので、短さより意味の伝わりやすさを取った。
- 2026-11-22 S1 と S2 を 1 回の委譲にまとめた。プールは `ensure` の流れに組み込まれており、分けると `ensure` を書き直すことになるため。結合テストはまとめて実行した。
- 2026-11-22 名前の置き換えは `sed` と `git mv` で行った。決まった文字列の置換で、判断を含まないため。
- 2026-11-22 MCP サーバーは今回の範囲に入れていない。
- 2026-11-22 状態は PostgreSQL のコメントに置き、手元に状態ファイルを持たない。複数の作業ツリーやエージェントから同じサーバーを使っても食い違わない。
- 2026-11-22 `DATABASE_URL` を管理用の接続先として既定では読まない。アプリ用の接続先に管理の SQL を流す事故を避ける。
- 2026-11-22 引数の解釈は標準ライブラリで行い、依存を pgx だけにする。
- 2026-11-22 15〜17 の既定は `WAL_LOG`。Omni 17 で `FILE_COPY` が 2.5〜9.9 秒とばらついた。
- 2026-11-22 計測のために Homebrew で `postgresql@18`（keg-only）を入れ、Docker Desktop を起動した。

## Notes

- 探りは `CREATEDB` だけのロールで実行した。スーパーユーザーでの計測結果（速度）はそのまま使える。
- 落とした仮説は無い。Omni の強制削除の失敗は、最初の仮説（内部の接続を切れない）を、スーパーユーザーの接続をつなぐ実験で確かめた。
- Docker Desktop の VM はカーネル 5.15 で、XFS は `bigtime=0,inobtcount=0,rmapbt=0,nrext64=0` を付けないとマウントできなかった。

## Review

2026-11-22、コミット 9517177（中核とプール）を `reviewer` が DESIGN.md と照合した。致命的な指摘は無く、削除の安全性は成り立つとの評価だった。指摘はコミット add98f6 で反映した。

| 指摘 | 対応 |
|---|---|
| M1 開いたベースへの `base adopt` が世代を上げずに閉じる | `base_open` を返すようにした。`TestAdoptRefusesOpenBase` |
| M2 接続の確認と接続禁止のあいだに入った接続が残る | 接続禁止のあとに数え直し、残れば開いた状態へ戻す。`TestCloseRevertsWhenSessionSurvives` |
| M3 不正な UTF-8 のブランチ名で作ったデータベースを管理できなくなる | ブランチ名の検査で拒否する |
| m1 ベースが開いていると既存ブランチへの `ensure` も失敗する | 既存ブランチは返す。`TestEnsureExistingBranchWhileBaseOpen` |
| m2 エラーが一律で原因が分からない | サーバーのメッセージと SQLSTATE を出す。`database_exists`、`server_resources`、`canceled` を追加 |
| m3 中断で目印の無い `tmp` が残りうる | キャンセル要求を送る設定にし、`tmp` の目印は中断されない文脈で書く。実機での中断の再現は未実施 |
| m4 ロックが管理用データベースごと | `doctor` の `note` と DESIGN.md に明記 |
| m5 後片付けの失敗で成功した結果が失われる | `warnings` に回す |
| m6 名前が重なるベース同士で `unmanaged` の誤報告 | ベース名の `__` を禁止。`TestTwoBasesAreIsolated` |
| m7 `refill_started` が起動前に書かれる | `refill_requested` に改名 |
| m8 プール由来のブランチで付け直しの警告が見えない | 目印に件数を持たせる。`TestPoolClaimReportsReplicationWarnings` |
| m9 `--ttl` が 0 と負を受け付ける | 拒否する。期限はサーバーの時刻で判定 |
| m10 接続の集計に内部のバックエンドが入る | クライアントの接続に限定 |
| m11 設計との細かな差 | `.pgcheckout.json` の探索を `.git` で止める。`git symbolic-ref` で初回コミット前も扱う |

レビュー後に見つけた不具合: AlloyDB Omni 17 で `drop --force` が 6 回中 2 回、権限不足で失敗した。Omni の内部の接続（ロール `alloydbadmin`）が在る瞬間は、一般のロールでは切れないためである。スーパーユーザーの接続をつないで再現し、強制削除の再試行で直した（コミット「fix: retry forced drops ...」）。

2026-11-22、修正後の差分（9517177..75da718 の `internal/branch`、`internal/pg`、`internal/naming`、`internal/marker`）を `reviewer` が再レビューした。重大な指摘は無い。削除の安全性、ベースを閉じる処理の終了状態、`ensure` の順序、M1〜M3 の修正は成り立つとの評価だった。中断時の後始末だけは厳密には成り立たないとされ、次のとおり反映した。

| 指摘 | 対応 |
|---|---|
| r1 目印の書き込みが失敗すると目印の無い `tmp` が残る | 3 回まで試す。失敗したらエラーに名前を含める |
| r2 中断にサーバーが応えないと目印の無い `tmp` が残りうる | 別の接続で有無を調べ、在れば目印を付ける。付けられなければエラーに名前を含める |
| r3 名前の付け替え中の中断で接続が切れ、`tmp` の片付けが走らない | 付け替えの前に中断を確かめ、付け替えは中断されない文脈で実行する |
| r4 失敗した `base adopt` が開いたベースの目印を残す。失敗した `base create` が開いたベースを残す | adopt は元のコメントと接続の可否を戻す。create は `tmp` のうちに閉じ、最後にベースへ付け替える。`TestAdoptFailureRestoresDatabase`、`TestAdoptFailureRestoresNullComment`、`TestCreateBaseCanceledLeavesNoBase` |
| r5 ACL の読み取りに失敗すると、`PUBLIC` の権限を残した複製が出回る | 読み取りの失敗を取り消しの失敗として扱い、複製を捨てる |
| r6 `base close` が確定したあとの掃除の失敗でコマンドが失敗する | `warnings` に回す |
| r7 `a_` と `a` のベースで名前が重なる | 末尾が `_` のベース名を拒否する |
| r10 強制削除でも使用中のとき、ヒントが `--force` を勧める | ヒントを分けた |
| r11 成功したあとの接続を閉じる失敗でコマンドが失敗する | 成功時は閉じる失敗を捨てる |

反映しなかった指摘: r8（期限はクライアントの時刻で書き、サーバーの時刻で比べる。ずれは期限が前後するだけで害が無い）、r9（負けた複製の警告が既存の結果に付く）、r12（接続禁止と接続の数え直しのあいだのごく短い窓。複製元に接続が在ると `CREATE DATABASE` が拒むので複製は壊れない）、r13 の一部（強制削除の再試行と ACL の読み取り失敗は、結合テストが無い）。

r1〜r11 の修正は、2026-11-23 に `reviewer` の再レビューを受けた（下の「未確認だった項目の確認」）。

修正後に実機で確かめたこと（2026-11-22、約 1 GB のベース、`wal_log`）:

- 複製の途中で `base create`、`ensure`、`pool fill` に SIGINT を送った。PostgreSQL 18.7 と AlloyDB Omni 17.10 のどちらでも、3 つとも `canceled` で終わり、`tmp` もベースも残らなかった
- PostgreSQL 18.7 で `CREATE DATABASE` を実行中のバックエンドを SIGSTOP で止めてから `ensure` に SIGINT を送った。5 秒後に `canceled` で終わり、エラーに `tmp` の名前と手動で削除する案内が出た。バックエンドを再開したあと、`tmp` は残らなかった
- AlloyDB Omni 17.10 で結合テストを `-count=5` で実行し、exit 0 だった
- r1（目印の書き込みの再試行）と、サーバーが `CREATE DATABASE` を完了させたあとに目印を付け直す経路は、2026-11-23 に障害を差し込むプロキシで再現した（下の「未確認だった項目の確認」）

2026-11-23、MCP サーバー（コミット 18d0683）を `reviewer` がレビューした。重大な指摘は無い。パスワードの扱い、標準出力に余計な出力が混ざらないこと、既存のコマンドの挙動が変わっていないことは成り立つとの評価だった。

| 指摘 | 対応 |
|---|---|
| c1 補充の子プロセスが、サーバーの終了までゾンビとして残る | 起動後に `Wait` で回収する。実機で、補充の完了後に子プロセスが残らないことを確かめた |
| c2 出力先が閉じると SIGPIPE でプロセスが終わり、後始末が飛ぶ | `mcp` では SIGPIPE を無視する。実機で、複製中に別の呼び出しの応答が書けなくなる状況を作って比べた。修正前のバイナリは終了コード 141 で終わり、目印の無い `tmp` が残った。修正後は終了コード 1 で終わり、`tmp` もブランチも残らなかった |
| c3 引数の型違いが JSON-RPC のエラーになり、モデルに理由が見えない | `usage` のツールの失敗にし、検証のメッセージを含める |
| c4 エラーの文言が CLI のフラグ名になっている | `ttl`、`env_file`、`env_key` は引数名で返す |
| c6 `mcp` だけ `--format` の検査を通らない | ほかのコマンドと同じ検査にした |

反映しなかった指摘: c5（パスワードが短い語や一般的な語のとき、メッセージ中の同じ語も伏せ字になる。漏れる向きの不具合ではない）、c7（結果の符号化の前に補充が始まる。失敗は理論上だけ）。

c1〜c6 の修正は 2026-11-23 に `reviewer` の再レビューを受け、Claude Code と Codex CLI からの接続も確かめた（下の「未確認だった項目の確認」）。デスクトップアプリなどの設定への登録は試していない。


### 未確認だった項目の確認（2026-11-23）

検証は PostgreSQL 18.7 で行った。検証用のプロキシと設定は一時ディレクトリに置き、コミットしていない。

実際の MCP クライアント（設定には登録せず、実行ごとの設定で起動し、送受信を記録した）:

| クライアント | 接続の手順 | 結果 |
|---|---|---|
| Claude Code 2.1.316（`claude -p --mcp-config … --strict-mcp-config`） | `server/discover`（2026-07-28）と `subscriptions/listen` | ツール 8 件を受け取り、10 回の呼び出しのうち 9 回が成功、引数なしの `drop` は `usage` の失敗。標準出力にパスワードは現れず、`env_file` のファイルにはパスワードが入った |
| Codex CLI 0.169.1（`codex exec -c mcp_servers.…`） | `initialize`（2025-06-18） | ツール 8 件を受け取り、`doctor` と `ensure` が成功、存在しないブランチの `dsn` は `branch_not_found`。`drop` は Codex 側が承認を要求して送らなかった（`exec` は承認できない） |

目印の再試行と付け直し（管理用の接続先を、障害を差し込む TCP プロキシに向けた）:

| 差し込んだ障害 | 結果 |
|---|---|
| 目印の `COMMENT` を 1 回だけ失敗させる | 2 回目で目印が付き、`ensure` は成功 |
| 目印の `COMMENT` を 3 回失敗させる | 一時データベースの名前と手動削除の案内が入った失敗。目印の無い一時データベースが残る（設計どおり） |
| 目印の `COMMENT` で接続を無応答にする | 10 秒後に失敗。一時データベースは残らない |
| サーバーが `CREATE DATABASE` を終えた応答を捨てて接続を切る | 別の接続で `tmp` の目印が付く。`pool drain` で削除できた。`gc` は作成から 1 時間たつまで削除しない |
| `CREATE DATABASE` の実行中に接続を切る | 名前入りの失敗。pgx が送る取り消し要求が届けば複製は中止されて何も残らない。取り消し要求も届かない場合は、目印の無い一時データベースが残り、`doctor` が `unmanaged` として報告する |

修正の再レビュー（`reviewer`、対象は 8613eda と 05aeb34）: Critical は無く、削除の安全規則を破る経路も見つからなかった。指摘ごとに、起きる頻度と起きたときの被害で直すかどうかを決めた（2026-11-23）。

| 指摘 | 頻度 | 被害 | 判断 |
|---|---|---|---|
| d1（Major）`TestCreateBaseCanceledLeavesNoBase` は接続の前に取り消すので、修正が無くても通る | `base.go` と `copy.go` を変えるたび | 取り消し後の後始末が壊れても気付けない | 直した |
| d2（Major）目印の再試行と付け直しに自動テストが無い | 同上 | 目印の無い複製を残さないための経路が壊れても気付けない | 直した |
| d3（Minor）付け直しで目印が付いた一時データベースが、失敗のメッセージに名前が出ないまま残る | 複製の完了と同時に接続が切れたとき（まれ） | ベースと同じ大きさのデータベースが、利用者に知らされずに 1 時間以上残る | 直した（名前と消し方を失敗に入れた。削除はしない） |
| d4（Minor）付け直しは名前だけで対象を決める。同じ名前でコメントの無い他人のデータベースへ目印を付けうる | 予約した接頭辞と 32 ビットの乱数が一致する名前を、他人が同じ時間帯に作った場合だけ | 他人のデータベースが `gc` の対象になる | 見送り。頻度が実質ゼロで、その名前を作れる権限があれば目印も自分で書ける |
| d5（Minor）`CREATE` が実行中のまま失敗を返す場合、待たずに手動削除を案内する | 複製の途中で接続が切れ、取り消し要求も届かないとき（まれ） | 手動の削除が 1 回要る。名前は失敗に出ており、`doctor` も報告する | 見送り |
| d6（Minor）コミットの応答が失われると、`base adopt` と `base create` の失敗の内容が実際の状態と食い違いうる | コミットの瞬間に接続が切れたとき（まれ、未再現） | 再実行すると `database_exists` か変更なしになる。データは失われない | 見送り |
| d7（Minor）SIGPIPE の扱いと子プロセスの回収に自動テストが無い | `cli` の起動まわりを変えるとき（少ない） | 補充の子プロセスが残る、出力先が閉じたときの終了が乱れる。手動で確認済み（c1、c2） | 直した（`TestMCPSurvivesClosedStdout`、`TestMCPRefillIsReaped`） |
| 細かい指摘 5 件（ACL の読み取り失敗が `insufficient_privilege` になる、など） | — | 文言と重複だけで、動作は正しい | 見送り |

d1〜d3 の修正（2026-11-23）:

- `internal/testpg/faultproxy.go` に、障害を差し込むプロキシを置いた。テストごとに `127.0.0.1:0` で待ち受け、差し込んだ回数を数える。
- 結合テスト 4 件: `TestCopyMarkerRetrySucceeds`、`TestCopyMarkerRetryExhausted`、`TestCopyRecoveryMarksAndNamesLeftover`、`TestCreateBaseCanceledLeavesNoBase`（中身を差し替え。一時データベースへの最初の `COMMENT` を見た時点で取り消す）。
- `copy.go`: 付け直しで目印が付いた場合の失敗に、一時データベースの名前と、`gc` と `pool drain` で消せることを入れた。
- 結果: PostgreSQL 18.7（`-race`）と AlloyDB Omni 17.10 で `go test -tags integration ./...` が通った。新しい 4 件は PostgreSQL 18 で 10 回、Omni で 5 回くり返して通った。
- 修正を外したコピーで、テストが落ちることを確かめた。再試行を 1 回にすると再試行の 2 件が、取り消し後の破棄を外すと `TestCreateBaseCanceledLeavesNoBase` が、名前を入れる分岐を外すと `TestCopyRecoveryMarksAndNamesLeftover` が落ちた。

残っていた確認の追加（2026-11-23、コミット 4d24d3f とその次）:

- `TestCreateBaseCanceledBeforeRenameLeavesNoBase`: 接続の禁止と名前の付け替えの間で取り消す。`base.go` のその位置の破棄を外したコピーでは落ちた。
- `TestCreateBaseRecoveryLeftoverIsRemovableAfterBaseExists`: `base create` の途中で残った一時データベースは、ベースが無いあいだ `gc` も `pool drain` も `base_not_found` になり、ベースを作り直したあとの `pool drain` で消える。
- `TestCopyMarkerBlackholedLeavesNoUnmarkedCopy`: 目印の書き込みで接続が無応答になっても、約 10 秒で失敗が返り、一時データベースは残らない。
- `TestMCPSurvivesClosedStdout` と `TestMCPRefillIsReaped`（d7）: `ignoreSIGPIPE()` と `refill.Wait()` をそれぞれ外すと落ちることを確かめた。
- 接続が切れたときの失敗が `internal` の `could not complete the operation` になる件を直した。操作の終わりに接続が閉じていて、失敗が未分類のままなら、`connect_failed` にして原因の文言を入れる（`service.go` の `connectionLost`）。
- 結果: PostgreSQL 18.7（`-race`）と AlloyDB Omni 17.10 で `go test -tags integration ./...` が通った。
- 自動テストに無いもの: 複製の実行中に接続が切れ、取り消し要求も届かない場合。複製が終わる時刻との競争になり、決定的なテストにできない。2026-11-23 にプロキシで手動の確認をしている。
- Codex は 2026-11-23 に利用上限に達した（再開は 11-24 0:22）。追加のテスト 5 件は、代替の経路どおり Opus のエージェントが書いた。

### 状態モデルと TLA+ の検証（2026-11-24）

依頼は「暗黙の状態遷移をすべて見つけて明示的なモデルに書き直す」「TLA+ のモデルを作ってすべてのケースを検証する」の 2 つ。

- [x] 状態と遷移の洗い出し。`internal/branch` の全ファイル、`internal/marker`、`internal/pg` の `sql.go`、`strategy.go`、`catalog.go`、`internal/gitx` の `hook.go`、`env.go`、`internal/mcpserver/server.go`、`internal/cli/run.go`、`DESIGN.md` は全文を読んだ。`mcpserver` の `tools.go` と `operations.go`、`pg/acl.go`、`gitx/file.go` は該当の関数だけを読んだ。`pg/settings.go`、`cli` の引数とシグナルのファイル、`config`、`naming` は読んでいない。成果物は `spec/STATE_MODEL.md`（9 つの状態機械、遷移ごとに行番号）
- [x] TLA+ の仕様 `spec/pgcheckout.tla`、設定 26 個、実行用の `spec/check.sh`
- [x] TLC 2.19 での全探索。違反なしを期待する 11 設定は違反なし（`normal` は 84,807,375 状態、深さ 37）。違反を期待する 15 設定は違反あり。`spec/check.sh` の終了コード 0
- [x] 対照実験。仕様の条件を 1 か所ずつ外した 13 件のうち 12 件で違反。残り 1 件（`ensure` の 1 回目のベースの確認）は、2 回目の確認が補うので違反にならない
- [x] 反例の確認。ベースの作り直しは PostgreSQL 18.7 で再現した（`ensure b2` が `source: pool`、`stale: false`、中身は作り直す前の内容）。`reset` の件はコードを読んで確認し、実機では再現していない

分かったこと（コードは変更していない）:

1. ベースを `DROP DATABASE` で消して作り直すと世代が 1 に戻り、前のベースから作ったプールが新しいものとして割り当てられる。目印にベースの OID が無いことが原因
2. `reset` は、ブランチを消したあとの作り直しが `base_open`、取り消し、接続の喪失で失敗すると、ブランチが無いままになる

検証していないこと: 上限（3 プロセス、2 ブランチ、操作 4 回、世代 3）を超える場合、終わることの保証（活性）、ブランチやプールへの接続が残っている場合、フックと接続先のファイル、MCP サーバーの終了手順。

判断:

- 「明示的なモデルに書き直して」は、文書と TLA+ の仕様として書き直す意味に取った。Go のコードを状態機械へ書き換える読み方もあるが、変更の量が数倍になるので採らなかった
- TLA+ の仕様は、Codex へ渡さずに主セッションで書いた。仕様は設計の成果物であり、TLC の結果を見ながら直す往復が要るため
- `tla2tools.jar`（v1.7.4、公式リリース）は、利用者の承認を得て作業用の一時フォルダへダウンロードした。リポジトリには入れていない

### ベースの作り直しの修正（2026-11-24）

依頼は「`spec/` と記録のコミット」と「ベースの作り直しの問題の修正」の両方。

- [x] `spec/` と記録のコミット（`22f7853`）
- [x] 修正。`base create` と `base adopt` は、同じベース名のプールとブランチに残る世代の最大値 + 1 から始める（`internal/branch/base.go` の `firstGen`）。目印の形式と、世代を比べる箇所は変えていない
- [x] 結合テスト 2 件（`TestBaseRecreatedStartsAfterLeftoverGen`、`TestBaseAdoptStartsAfterLeftoverGen`）。修正を外すと 2 件とも落ちることを確かめた
  - 証拠: `go build`、`go vet`、`go vet -tags integration`、`go test ./...`、`gofmt -l internal` が exit 0。`go test -tags integration ./...` が PostgreSQL 18.7（`-race`）と AlloyDB Omni 17.10 で exit 0。新しい 2 件と `TestCreateBaseRecoveryLeftoverIsRemovableAfterBaseExists` は PostgreSQL 18 で 5 回くり返して通った
- [x] 実機での再現手順のやり直し（PostgreSQL 18.7）。作り直したベースは世代 2、`ensure b2` は `source: copy`、`stale: false` で新しい内容、`list` は `b1` を `stale: true`、`pool status` は前のプールを `stale: 1` と数えた
- [x] モデルの修正と TLC の全設定の再実行。28 設定（違反なしを期待する 13、違反を期待する 15）がすべて期待どおりで、`spec/check.sh` は exit 0。`external` は 2,522,533 状態、`external-faults` は 33,309,473 状態。既存の 11 設定の状態数は修正の前と同じ
- [x] 対照実験。開始の世代を 1 に戻すと `external` が `Fresh` に違反する

判断:

- 目印にベースの OID を入れる案は採らなかった。目印の形式が変わり、古い目印の扱いと、世代を比べるすべての箇所の変更が要る。世代の開始値を変える案は、問題の場面でだけ挙動が変わる
- 数える対象はプールとブランチだけにした。`tmp` も数えると、`base create` の失敗で残った `tmp` だけで次のベースが世代 2 になり、既存のテストと案内文の挙動が変わる。`tmp` は、作ったプロセスが OID の比較（`ensure.go:133`、`pool.go:122`）を通らなければ割り当てられない
- 実装は `codex-sol` に渡した。数える対象を絞る 3 行と、Codex が足した結合テストの共通部分の変更の取り消しは、主セッションで行った

扱っていないこと:

- 目印が付いたままのベースが戻る場合（名前を変えて戻す、コメントごと復元する）。`base adopt` は何も変えずに受け入れるので、世代が重なりうる。モデルも検査していない
- 前のベースから作った `tmp` が、作り直したベースより大きい世代で残る場合。割り当てられることは無いが、`pool drain` でだけ消える
- `reset` がブランチを消したあとで失敗しうる件は、変更していない

### 残っていた弱点 3 件の修正（2026-11-25）

- [x] 世代がベースより大きい `tmp` を `gc` が消す（PR #3、マージ済み）
  - 証跡: `TestGCDropsAgedTmpFromNewerGeneration` は修正前に落ち、修正後に通る。CI は PostgreSQL 18 と AlloyDB Omni 17 で成功
- [x] 目印が付いたまま戻るベース（PR #4）
  - 完了条件: 新しい結合テスト 2 件が修正前に落ち、修正後に通る。TLC の設定 `reentry` が違反なし
  - 証跡: `TestParkedBaseKeepsGenerationsApart` と `TestRestoredBaseMarkerDoesNotReuseGeneration` は、修正を外すと落ちる。`reentry` は 29,139,569 状態で違反なし。`base close` を「世代 + 1」に戻すと `Fresh` に、古さの判定を元に戻すと `FreshCurrent` に違反する。退避したベースを最大値に入れる規則は、モデルの上限の中では違反を出さず、結合テストだけで確かめている
  - 扱っていないこと: 複製の途中でベースが退避されて戻り、その間に別のデータベースがベースの名前を持っていた場合。`CREATE DATABASE` はテンプレートを名前で指定するので、検出できない
- [x] `reset` を入れ替え方式にする
  - 完了条件: 複製の失敗、入れ替えの失敗、古いデータベースの削除の失敗のテストが通る。TLC の性質 `BranchKept` が全設定で成り立つ
  - 証跡: `TestResetKeepsBranchWhenCopyFails`、`TestResetRollsBackFailedSwap`、`TestResetWarnsWhenOldDatabaseCannotBeDropped` は、修正を外すと落ちる。`reset` のテストは `-race` で 3 回繰り返して通った。`reset` を「先に消す」形に戻すと、`union` は `BranchKept` に違反する
  - レビュー（`reviewer`、2026-11-25）: 致命的と重大の指摘は無し。中程度 4 件のうち、古いデータベースの削除の期限とロックの外への移動、警告の文言を直した。切断の競合の再試行も直した。テストが扱わない経路（強制の再試行、2 つのロックの間でベースが開く場合、取り消し）は、モデルの検査に頼っている

### マネージドの AlloyDB での計測（2026-11-28）

- [x] 無料トライアルのクラスタで、複製の速さと結合テストを確かめる
  - 環境: プロジェクト `poc-toshi0607`、クラスタ `pgcheckout-trial`（東京、PostgreSQL 18、トライアルは 2026-12-27 まで）。`postgres` ロールはスーパーユーザーではなく、`CREATEDB` と `CREATEROLE` を持つ
  - 結合テスト: `internal/branch` の 51 件のうち 41 件が通った。落ちた 10 件は、すべて障害を注入するプロキシを使うテストである。プロキシが平文で接続するので、SSL を必須とするサーバーに拒否された。製品の不具合ではない。`internal/mcpserver` は通った。取り残したデータベースは無い
  - 見つけたこと: `doctor` は、サーバーのバージョンだけから `file_copy_clone` と報告する。実際の複製は `file_copy` に切り替わり、結果にもそう出る。`doctor` の報告だけが実態と違う。直していない
