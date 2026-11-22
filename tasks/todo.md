# pgcheckout 計画

設計は `DESIGN.md`。検証コマンドはリポジトリのルートで実行する。

## Plan

- [ ] S1 中核: 設定、名前、目印、方式の選択、`ensure`（直接複製）、`list`、`dsn`、`drop`、`reset`、`base create/adopt/open/close`、`doctor`
  - 完了条件: `go build ./... && go vet ./... && go test ./...` が exit 0。`PGCHECKOUT_TEST_ADMIN_URL=... go test -tags integration ./...` が PostgreSQL 18 で exit 0
- [ ] S2 プール: `pool fill/status/drain`、`ensure` のプール利用と切り離した補充、`gc`、`--ttl`
  - 完了条件: S1 と同じコマンドが exit 0。`pgcheckout ensure | cat` が補充の完了を待たずに戻る
- [ ] S3 git 連携: `hook install`、`ensure --env-file`
  - 完了条件: 一時リポジトリで `git checkout` 後に env ファイルの接続先が切り替わる
- [ ] S4 AlloyDB Omni 17 で結合テスト
  - 完了条件: Omni 17 を指す `PGCHECKOUT_TEST_ADMIN_URL` で `go test -tags integration ./...` が exit 0
- [ ] S5 レビュー（`reviewer` に DESIGN.md との照合を依頼）と指摘の反映
- [ ] S6 README と AGENTS.md（エージェント向けの使い方）

## Constraints

| Constraint | Source | Verify by |
|------------|--------|-----------|
| 対象は PostgreSQL 互換だけ。MySQL は扱わない | user msg（2026-11-22） | DESIGN.md「対象にしないもの」 |
| ローカル（PostgreSQL、AlloyDB Omni）を最初の対象にする | user msg | S1、S4 の結合テスト |
| db-branch と実装が同じである必要はない。保守しやすく、速く、エージェントが使いやすいこと | user msg | 依存が pgx だけ、JSON 既定、冪等な ensure、対話なし |
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

## 判断の記録

- 2026-11-22 名前は仮に `pgtwig`。GitHub のリポジトリ検索で同名が無かった。変えたければ一括で置換できる。
- 2026-11-22 状態は PostgreSQL のコメントに置き、手元に状態ファイルを持たない。複数の作業ツリーやエージェントから同じサーバーを使っても食い違わない。
- 2026-11-22 `DATABASE_URL` を管理用の接続先として既定では読まない。アプリ用の接続先に管理の SQL を流す事故を避ける。
- 2026-11-22 引数の解釈は標準ライブラリで行い、依存を pgx だけにする。
- 2026-11-22 15〜17 の既定は `WAL_LOG`。Omni 17 で `FILE_COPY` が 2.5〜9.9 秒とばらついた。
- 2026-11-22 計測のために Homebrew で `postgresql@18`（keg-only）を入れ、Docker Desktop を起動した。

## Notes

- 探りは `CREATEDB` だけのロールで実行した。スーパーユーザーでの計測結果（速度）はそのまま使える。
- Docker Desktop の VM はカーネル 5.15 で、XFS は `bigtime=0,inobtcount=0,rmapbt=0,nrext64=0` を付けないとマウントできなかった。

## Review

（未実施）
