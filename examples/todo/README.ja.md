# todo のサンプル

[English](README.md) | 日本語

pgcheckout を試すための小さな todo アプリと、使い方の流れを最初から最後まで実行して、手順ごとに結果を確かめるスクリプトである。

## 流れを実行する

PostgreSQL 18 を起動する。サーバーは `127.0.0.1:54320` だけで待ち受ける。

```bash
docker compose -f examples/todo/compose.yaml up -d --wait
```

スクリプトは、リポジトリの中のどこからでも実行できる。

```bash
examples/todo/demo.sh
```

最後の行に `demo passed` と出る。確認に失敗した場合は、期待した値と実際の値を出して、終了コード 1 で終わる。

終わったら、サーバーを止める。

```bash
docker compose -f examples/todo/compose.yaml down
```

## スクリプトがすること

スクリプトは、`pgcheckout` と `todo` を一時ディレクトリにビルドし、そこに作った作業用の git リポジトリの中で動く。このリポジトリの git の状態と、利用者の git の設定には触れない。

| 手順 | コマンド | 確かめること |
|---|---|---|
| 1 | `todo createdb`、`todo migrate` | 開発用のデータベースに 3 行ある |
| 2 | `pgcheckout base create`、`pgcheckout pool fill` | プールに使える複製が 2 つある |
| 3 | `pgcheckout hook install`、`pgcheckout ensure` | `main` にデータベースが割り当てられ、`.env.pgcheckout` が書かれる |
| 4 | `git checkout -b feature/priority`、新しいマイグレーション | フックが `.env.pgcheckout` を書き換え、ブランチに新しい列と 4 行がある |
| 5 | `git checkout main` | `main` には、新しい列も新しい行も無い |
| 6 | `git checkout feature/priority` | ブランチのデータベースは、作り直されずに残っている |
| 7 | `pgcheckout base open`、ベースへのマイグレーション、`pgcheckout base close` | 両方のブランチが古いと報告され、どちらも作り直されない |
| 8 | `pgcheckout reset main` | `main` にベースの新しい列があり、古いという報告が消える |
| 9 | `pgcheckout drop`、`pgcheckout pool drain`、`pgcheckout gc` | この実行で作ったデータベースが残っていない |

## 別のサーバーで実行する

`PGCHECKOUT_ADMIN_URL` に、使うサーバーの管理用データベースを指定する。ロールには `CREATEDB` が要る。

```bash
PGCHECKOUT_ADMIN_URL='postgres://dev@127.0.0.1:5432/postgres' examples/todo/demo.sh
```

| 変数 | 意味 |
|---|---|
| `PGCHECKOUT_ADMIN_URL` | 管理用データベースへの接続文字列。既定は `compose.yaml` のサーバー |
| `DEMO_BASE` | ベースの名前。既定は `todo_demo` で、開発用のデータベースは `todo_demo_dev` になる |
| `KEEP=1` | 実行後も、データベースと作業用のリポジトリを残す |

ベースの名前で始まるデータベースがすでに在る場合、スクリプトは開始しない。削除するのは、自分で作ったデータベースだけである。

この流れは、PostgreSQL 18 とマネージドの AlloyDB 18.4 で通ることを確かめている。

## todo アプリ

`todo` は、接続文字列を `--url`、環境変数 `DATABASE_URL`、カレントディレクトリの `.env.pgcheckout` の `DATABASE_URL=...` の行、の順に探す。そのため、`pgcheckout hook install --env-file .env.pgcheckout` を実行したあとは、ほかの設定をしなくても `git checkout` に追従する。

| コマンド | 動作 |
|---|---|
| `todo migrate` | `migrations` の `*.sql` を、ファイル名の順に適用する |
| `todo add TITLE` | todo を 1 件足す |
| `todo list` | 列の名前を出し、続けて todo を 1 行ずつ出す |
| `todo createdb NAME`、`todo dropdb NAME` | データベースを作る、または消す。スクリプトは、`psql` の代わりにこれを使う |

## 自分のリポジトリでフックが動かない場合

git が `.git/hooks/post-checkout` を実行するのは、`core.hooksPath` が設定されていないときだけである。`core.hooksPath` をグローバルに設定している場合でも、`pgcheckout hook install` はフックを `.git/hooks` に書き、結果に `note` を付ける。このフックが動くのは、共有のディレクトリのフックが、リポジトリのローカルのフックを呼ぶ場合だけである。スクリプトは、利用者のグローバルの設定を読まずに git を実行することで、これを避けている。
