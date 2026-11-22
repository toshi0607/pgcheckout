# pgtwig

pgtwig は、PostgreSQL 互換のサーバーの中で開発用のデータベースを複製し、git のブランチごとに 1 つずつ割り当てる CLI である。
複製を先に作っておき、要求が来たら名前を付け替えて渡すので、ブランチ用のデータベースは数十ミリ秒で手に入る。

名前は仮のものである。

## 動作する環境

接続文字列で届く PostgreSQL 互換のサーバーで動く。
PostgreSQL 18.7 と AlloyDB Omni 17.10 で結合テストを実行している。
サーバー側に必要な権限は `CREATEDB` だけである。

複製の速さは、サーバーのバージョンとデータの置き場所で変わる。
1 GB のデータベースで計測した値を次に示す（各 1〜4 回の計測）。

| 環境 | 複製 | 名前の付け替え |
|---|---|---|
| PostgreSQL 18（macOS に直接インストール、APFS） | 43〜106 ミリ秒 | 0.12〜0.22 ミリ秒 |
| PostgreSQL 18（Docker、XFS のボリューム） | 43〜68 ミリ秒 | 0.54 ミリ秒 |
| PostgreSQL 18（Docker、既定の ext4） | 3.07 秒 | 未計測 |
| AlloyDB Omni 17（Docker、493 MB） | 0.95〜1.02 秒 | 0.8〜3.1 ミリ秒 |

複製が遅い環境でも、プールに複製が残っていれば、待ち時間は名前の付け替えと接続の分だけになる。

## 使い始める

ビルドする。

```bash
go build -o bin/pgtwig ./cmd/pgtwig
```

管理用の接続先とベースの名前を環境変数で渡す。
管理用の接続先には、複製元でもブランチでもないデータベース（`postgres` など）を指定する。

```bash
export PGTWIG_ADMIN_URL=postgres://dev@127.0.0.1:5432/postgres
```

```bash
export PGTWIG_BASE=app
```

既存の開発用データベース `app_dev` を複製して、ベース `app` を作る。
ベースは接続を禁止した状態で保存され、`app_dev` はそのまま使える。

```bash
pgtwig base create app --from app_dev
```

現在の git ブランチ用のデータベースを用意する。
何度実行しても、同じブランチには同じデータベースが返る。

```bash
pgtwig ensure
```

```json
{"branch":"feat/login","database":"app__feat_login_d668f008","url":"postgres://dev@127.0.0.1:5432/app__feat_login_d668f008","created":true,"source":"pool","stale":false,"gen":1,"base_gen":1,"strategy":"file_copy_clone","elapsed_ms":2,"refill_requested":true}
```

`refill_requested: true` の場合、pgtwig は結果を出力したあとにバックグラウンドでプールの補充プロセスを起動する。
起動に失敗すると標準エラーに `{"warning":{"code":"refill_failed","message":"..."}}` を出力し、終了コードは 0 のままとする。

## git のブランチ切り替えに連動させる

post-checkout フックを入れると、ブランチを切り替えるたびに接続先がファイルへ書き出される。

```bash
pgtwig hook install --env-file .env.pgtwig
```

`.env.pgtwig` には `DATABASE_URL=...` の 1 行が書かれる。
フックは、リポジトリの `.git/hooks` に置く。`core.hooksPath` が指す共有のディレクトリには書き込まない。

## ベースを更新する

マイグレーションなどでベースの内容を変えるときは、ベースを開いて変更し、閉じる。
閉じるとベースの世代が 1 増え、古い世代から作ったプールが破棄される。

```bash
pgtwig base open
```

```bash
pgtwig base close
```

既存のブランチは作り直されない。古い世代のブランチには、`ensure` と `list` の出力で `stale: true` が付く。
作り直すときは `pgtwig reset` を使う。

## コマンド

| コマンド | 動作 |
|---|---|
| `ensure [ブランチ]` | ブランチ用のデータベースを用意して接続先を返す |
| `dsn [ブランチ]` | 接続先を返す |
| `list` | ベース、ブランチ、プールの一覧 |
| `drop [ブランチ]` | ブランチを削除する。接続が残っていれば `--force` が要る |
| `reset [ブランチ]` | ブランチを削除して作り直す |
| `pool fill` / `pool status` / `pool drain` | プールの補充、状態、破棄 |
| `gc` | 期限切れのブランチと古い世代のプールを削除する |
| `doctor` | サーバー、権限、ベースの状態を報告する |
| `base create` / `base adopt` / `base open` / `base close` | ベースの作成と更新 |
| `hook install` / `hook uninstall` | post-checkout フック |

ブランチ名を省くと、git の現在のブランチ名を使う。

## 設定

| 環境変数 | フラグ | 意味 |
|---|---|---|
| `PGTWIG_ADMIN_URL` | `--admin-url` | 管理用データベースへの接続文字列 |
| `PGTWIG_BASE` | `--base` | ベースの名前 |
| `PGTWIG_POOL_SIZE` | `--pool-size` | プールの目標数（既定 2、0 で無効） |
| `PGTWIG_STRATEGY` | `--strategy` | 複製の方式（既定 `auto`） |

秘密でない値は、リポジトリの `.pgtwig.json` にも書ける。

```json
{"base": "app", "pool_size": 2}
```

`DATABASE_URL` は読まない。アプリケーション用の接続先に管理の SQL を実行する事故を避けるためである。

## 出力

標準出力には JSON のオブジェクトを 1 つだけ書く。
エラーは標準エラーに `{"error":{"code":"...","message":"...","hint":"..."}}` の形で書き、終了コードを 1（使い方の誤りは 2）にする。
入力を促す対話はしない。

## 削除の範囲

pgtwig が削除するのは、データベースのコメントに pgtwig の目印があり、指定したベースに属するブランチ、プール、複製途中のデータベースだけである。
ベースと管理用データベースは削除しない。
名前が規則に合っていても目印の無いデータベースは、`list` と `doctor` が `unmanaged` として報告するだけにする。

## 制限

- ブランチは同じサーバーの中の別のデータベースであり、CPU、メモリ、ロール、サーバーのバージョンを共有する。
- ブランチ同士のマージと差分は扱わない。
- AlloyDB Omni の Docker イメージは PostgreSQL 17 までなので、複製は容量に比例した時間がかかる。
- マネージドの AlloyDB では計測していない。
- 接続先の文字列にはパスワードが含まれる。出力やファイルの扱いに注意する。

## テスト

```bash
go test ./...
```

結合テストは、`CREATEDB` を持つロールの接続先を渡して実行する。

```bash
PGTWIG_TEST_ADMIN_URL=postgres://dev@127.0.0.1:5432/postgres go test -tags integration ./...
```

設計は [DESIGN.md](DESIGN.md) にある。
