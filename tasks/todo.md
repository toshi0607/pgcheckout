# pgcheckout 計画

設計は `DESIGN.md`。検証コマンドはリポジトリのルートで実行する。

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
- [ ] GitHub への公開は、トシの OK が出てから

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
| リポジトリはローカルのまま。GitHub への作成とプッシュはトシの OK が要る | rules（外へ出す操作） | `git remote -v` が空 |
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
| マネージドの AlloyDB で `clone` が速い | UNVERIFIED-ACCEPTED（2026-11-22） | 実クラスタは課金が要るので未計測。方式は自動選択で、遅くても動作は変わらない。トシに報告済み |
| AlloyDB Omni の Docker イメージに PostgreSQL 18 は無い | VERIFIED | Docker Hub のタグ一覧（最新は 17.10、2026-11-22 時点） |
| MCP の公式 Go SDK v1.8.0 は、標準入出力で `initialize` と `server/discover` の両方に応える | VERIFIED | 2026-11-23 の試作: 両方の手順で `tools/call` が `structuredContent` を返した |
| 標準入力が閉じたとき、実行中のツールは中断され、後始末を待ってから終了する | VERIFIED | 2026-11-23 の実機確認: 複製中に標準入力を閉じて exit 0、`tmp` は残らず。サーバー側でも文脈を中断して待つ |

## 判断の記録

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

修正の再レビュー（`reviewer`、対象は 8613eda と 05aeb34）: Critical は無く、削除の安全規則を破る経路も見つからなかった。指摘は次のとおりで、どれも未対応である。

| 指摘 | 状態 |
|---|---|
| d1（Major）`TestCreateBaseCanceledLeavesNoBase` は接続の前に取り消すので、修正が無くても通る | コードを読んで確認した。挙動そのものは、複製中の SIGINT で実機確認済み |
| d2（Major）目印の再試行と付け直しに自動テストが無い | 上のプロキシで手動の確認はした。自動テストは無い |
| d3（Minor）付け直しで目印が付いた一時データベースが、失敗のメッセージに名前が出ないまま残る。`base create` の場合はベースが無いので `gc` も `pool drain` も届かない | `ensure` の場合を実機で確認した。メッセージは `could not complete the operation` だけだった |
| d4（Minor）付け直しは名前だけで対象を決める。`CREATE` を送る前に取り消された場合などに、同じ名前でコメントの無い他人のデータベースへ目印を付けうる（接尾辞は 32 ビットの乱数） | 未確認（コードを読んだ指摘） |
| d5（Minor）`CREATE` が実行中のまま失敗を返す場合、待たずに手動削除を案内する | 上の表の 5 行目で確認した |
| d6（Minor）コミットの応答が失われると、`base adopt` と `base create` の失敗の内容が実際の状態と食い違いうる | 未確認（推論による指摘） |
| d7（Minor）SIGPIPE の扱いと子プロセスの回収に自動テストが無い | 手動の確認だけ（c1、c2） |

ほかに、接続が切れたときの失敗が `internal` の `could not complete the operation` になり、原因が読み取れないことを確認した。
