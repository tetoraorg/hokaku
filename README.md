# hokaku — initializeから60秒を記録する

Goアプリに組み込むmoduleです。`Recorder.Start()`がNGINX・MySQLの専用ログを切り替え、アプリ自身のfgprofを開始します。その後はgoroutineがgopsutilで1秒ごとにホストのCPU・メモリを記録し、60秒後に計測を止め、4ファイルと検証用manifestをkosakana中央サーバーへ送ります。

Go 1.26以上が必要です。依存はfgprof v0.9.5、gopsutil/v4 v4.26.9に固定しています。主対象はLinuxです。macOSでの開発検証は`darwin-host`として記録し、Linuxの観測とは区別します。OTel Collectorやdoolのインストールは不要です。

公開Go moduleは `github.com/tetoraorg/hokaku`、Goのpackage名は `hokaku` です。受信API・ダッシュボードは別途kosakanaサーバーが必要で、このリポジトリには含まれません。利用には構築済みサーバーへのアクセスが必要です。サーバーの管理者から接続先URLとingest権限のAPIキー、対応するNGINXのJSONログ設定・MySQLのslow log設定・ログ切り替えヘルパーを受け取ってください。以下はこれらの準備を終えた環境向けの手順です。

```sh
go get github.com/tetoraorg/hokaku@latest
```

## モジュール構成

- `capture.go` はRecorderの開始・終了と計測順序を管理します。`sampler.go` はCPUとメモリを同じ時刻・ホスト情報で1行にまとめ、`host.jsonl`へ書き込みます。
- `internal/cpu` と `internal/memory` はgopsutilからの取得・検証、`internal/nginx` と `internal/mysql` は入力ログとartifact名の対応付けを担当します。
- 共通の`internal/logs`がローテーション検出と期限時点のスナップショットを行い、`internal/fgprof`がprofileファイルと停止処理を管理します。
- `manifest.go` はartifact検証とmanifest公開、`upload.go` は完成したcaptureの送信を担当します。

## 単体コマンドで実行する

`cmd/recorder`は起動直後に1回の計測を開始し、中央への送信完了を待って終了します。アプリへ組み込まず、ホストのCPU・メモリとローカルのNGINX・MySQLログを採る場合に使います。**単体コマンドのfgprofはrecorder自身のprofileです。別プロセスで動くアプリのprofileを採る場合は、次節の方法でアプリへ組み込んでください。** initializeとの同期や、他ホストへの開始通知は呼び出し側で行います。

リポジトリルートからビルドし、計測対象のホストで実行します。ログの準備は[ログ切り替えの準備](#ログ切り替えの準備)を参照してください。

```sh
go build -o recorder ./cmd/recorder
export KOSAKANA_ENDPOINT='http://127.0.0.1:3000'
export KOSAKANA_ACCESS_KEY='生成したingest権限のキー本体'
./recorder \
  --environment practice-1 \
  --host app-1 \
  --output-dir /home/isucon/kosakana-captures \
  --git-dir /home/isucon/webapp \
  --access-log /var/log/nginx/kosakana-access.log \
  --slow-log /var/log/mysql/kosakana-slow.log \
  --duration 60s \
  --interval 1s \
  -- /usr/bin/sudo -n /usr/local/sbin/kosakana-rotate-logs
```

`--`以降はログ切り替えコマンドとその引数です。shellを介さず実行します。ログパスを指定する場合はコマンドが必須で、両方を省略する場合はコマンドも省略できます。SIGINT／SIGTERMを受けると計測・送信を中断して後処理を待ちます。同期実行中のログ切り替えは終了または10秒の期限まで待ちます。送信成功時の終了コードは`0`、設定エラー・計測失敗・送信失敗・中断時は`1`です。

| CLIフラグ       | 対応する`Config`    | 既定値                                        |
| --------------- | ------------------- | --------------------------------------------- |
| `--endpoint`    | `Endpoint`          | 環境変数`KOSAKANA_ENDPOINT`。フラグ指定を優先 |
| `--environment` | `Environment`       | なし（必須）                                  |
| `--host`        | `Host`              | なし（必須）                                  |
| `--output-dir`  | `OutputDir`         | なし（必須）                                  |
| `--git-dir`     | `GitDir`            | 空（commit 情報を収集しない）                  |
| `--access-log`  | `AccessLog`         | 空（収集しない）                              |
| `--slow-log`    | `SlowLog`           | 空（収集しない）                              |
| `--duration`    | `Duration`          | `60s`                                         |
| `--interval`    | `Interval`          | `1s`                                          |
| `--capture-id`  | `StartWithID`の引数 | 空（IDを自動生成）                            |

トークンは環境変数`KOSAKANA_ACCESS_KEY`で指定します。`--capture-id`には半角英数字と`_`・`.`・`-`を使った1〜128文字の新しいIDを指定し、`.`と`..`は使えません。複数ホストには同じIDを渡してください。全フラグは`./recorder --help`でも確認できます。各設定の制約は[設定値一覧](#設定値一覧)にまとめています。

## 未知形式のログを同梱する（Go module）

Goの `hokaku.Config.RawLogs` に、ログの識別名とファイルパスを指定します。

```go
RawLogs: map[string]string{
    "powerdns": "/var/log/pdns.log",
},
```

既存のConfigに上記を追加すると、`raw-powerdns.log` が通常の計測ファイルと同梱されます。JSONなどの形式や末尾改行を要求せず、中央サーバーの `<capture保存先>/<capture ID>/<host>/raw-powerdns.log` に原文を保存します。HTTP・SQLの集計には取り込みません。受信側もRawLogs対応版に更新してください。

対象は通常ファイルです。`Start` 内で既存の `Rotate` コールバックを実行した後、ファイルを開いた時点から計測終了までの追記分を採ります。開始前の内容は送信しません。RawLogsのためのローテーションは不要ですが、Configの `Rotate` は引き続き必須です。既知ログを採らない場合は `func(context.Context) error { return nil }` を指定できます。計測中に対象ファイルがローテーション・切り詰めされた場合や、ファイルを開けない場合はcaptureを失敗にします。

識別名は先頭が半角英数字、以降は半角英数字・`_`・`.`・`-` の1〜64文字、最大16ファイルです。チェックサム検証は追加ログにも適用されます。`Recorder.Upload` で再送する際はmanifestの追加ログも送信するため、RawLogsを再指定する必要はありません。

## アプリへ組み込む

アプリのディレクトリで公開moduleを追加します。

```sh
go get github.com/tetoraorg/hokaku@latest
go mod tidy
```

Recorderはプロセス起動時に1個だけ作り、initialize処理で再利用します。中央サーバーの設定ページで`ingest`権限のAPIキーを発行します。キー本体はアプリの環境変数`KOSAKANA_ACCESS_KEY`へ渡してください。

```go
import (
    "log"
    "os"

    "github.com/tetoraorg/hokaku"
)

recorder, err := hokaku.New(hokaku.Config{
    Endpoint:    os.Getenv("KOSAKANA_ENDPOINT"),
    Token:       os.Getenv("KOSAKANA_ACCESS_KEY"),
    Environment: "practice-1",
    Host:        "app-1",
    OutputDir:   "/home/isucon/kosakana-captures",
    GitDir:      "/home/isucon/webapp",
    AccessLog:   "/var/log/nginx/kosakana-access.log",
    SlowLog:     "/var/log/mysql/kosakana-slow.log",
    Rotate: hokaku.CommandRotator(
        "/usr/bin/sudo", "-n", "/usr/local/sbin/kosakana-rotate-logs",
    ),
    OnComplete: func(result hokaku.Result) {
        if result.Err != nil {
            log.Printf("kosakana capture %s failed: %v (files: %s)",
                result.ID, result.Err, result.Directory)
            return
        }
        log.Printf("kosakana capture uploaded: %s", result.ID)
    },
})
if err != nil {
    log.Fatal(err)
}
// アプリ停止時に呼ぶ。OnCompleteの中からCloseを呼ばない。
defer recorder.Close()
```

initializeのDBリセットなどが終わった後、レスポンスを返す前に呼びます。

```go
if id, err := recorder.Start(); err != nil {
    // 計測の失敗でアプリの初期化結果を変えない例。
    log.Printf("kosakana capture could not start: %v", err)
} else {
    log.Printf("kosakana capture started: %s", id)
}
```

`Start`はGit情報の取得（指定時）、ログ切り替えとfgprof開始までを同期的に行います。Git情報の取得とログ切り替えのタイムアウトはそれぞれ10秒です。**60秒の計測やファイル送信が終わるのをinitializeで待つ必要はありません**。リクエストcontextの終了で計測が止まることもありません。

`GitDir` にアプリの Git checkout を指定すると、capture 開始時の HEAD の SHA・commit 件名・未コミット変更の有無を `manifest.json` の `git` に同梱します。実行環境に `git` と HEAD のある checkout が必要です。Git 情報の取得に失敗すると、ログ切り替え前に `Start` がエラーを返します。空なら収集しません。

ステージ済み・未ステージ・未追跡ファイルがある場合は `slog.Warn` で warning を出し、送信する件名を `[+]<件名>` にします（例：`[+]feat: add index`）。Git の ignore 対象は含めません。差分やファイル内容は送信しません。checkout が実行中バイナリと対応するよう、呼び出し側で管理してください。計測中の変更は反映せず、再送でも開始時の情報を維持します。出力先やログは checkout の外に置くか ignore 対象にしてください。

先に kosakana を Git 情報の受信に対応した版へ更新してください。ベンチ一覧・詳細に件名と commit リンクを表示します。複数ホストの SHA が異なる場合は、kosakana に `⚠ Multiple commits` と各 SHA を表示します。

同じプロセスで計測・送信中に再度Startすると`ErrBusy`です。上書きや多重ローテーションは行いません。`Close`は計測・送信を中断し、終了を待ちます。途中終了した計測にはmanifestを作らず、送信しません。

## 設定値一覧

設定は`hokaku.New(hokaku.Config{...})`へ渡します。module自身は環境変数や設定ファイルを読み込みません。上の例の`KOSAKANA_ENDPOINT`と`KOSAKANA_ACCESS_KEY`は、組み込み先のアプリが`os.Getenv`で読み取って渡しています。設定はRecorder作成時に確定します。

| フィールド    | 型                            | 必須・既定値                        | 用途・制約                                                                                                                                                                                                                   |
| ------------- | ----------------------------- | ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Endpoint`    | `string`                      | 必須                                | 中央サーバーのorigin URL（例: `https://kosakana.example.com`）。パスは空または`/`のみで、ユーザー情報・クエリ・fragmentは指定できません。HTTPは`127.0.0.1`・`localhost`・`::1`のみ許可します。ポート指定は可能です。         |
| `Token`       | `string`                      | 必須                                | 送信時のBearer token。中央サーバーの設定ページで発行したingest権限のキー本体を指定します。                                                                                                                                   |
| `Environment` | `string`                      | 必須                                | 環境名（例: `practice-1`）。1〜64文字。使える文字は半角英数字と`_`・`.`・`-`で、`.`と`..`だけの名前は禁止です。                                                                                                              |
| `Host`        | `string`                      | 必須                                | ホスト名（例: `app-1`）。1〜128文字。文字種と禁止名は`Environment`と同じです。ホストごとに異なる名前を指定します。                                                                                                           |
| `OutputDir`   | `string`                      | 必須                                | ローカル保存先の親ディレクトリ。`Start`時に未作成なら作成します。アプリの実行ユーザーに書き込み権限が必要です。相対パスはプロセスの作業ディレクトリを基準にします。                                                          |
| `AccessLog`   | `string`                      | `""`                                | NGINXの専用JSONログのパス。空なら`access.log`を空のartifactにします。指定したログには読み取り権限が必要です。                                                                                                                |
| `SlowLog`     | `string`                      | `""`                                | MySQLの専用FILE slow logのパス。空なら`slow.log`を空のartifactにします。指定したログには読み取り権限が必要です。                                                                                                             |
| `Rotate`      | `func(context.Context) error` | 必須                                | ログ切り替えとreopenを行うコールバック。`Start`内で同期実行され、エラーなら開始を中止します。渡されるcontextの期限は10秒です。`hokaku.CommandRotator(...)`で固定コマンドを指定できます。                                    |
| `Duration`    | `time.Duration`               | `0`なら`60 * time.Second`           | ログ切り替えとfgprof開始後の計測時間。`2 * Interval`以上、`240 * time.Second`以下にします。送信時間は含みません。                                                                                                            |
| `Interval`    | `time.Duration`               | `0`なら`time.Second`                | ホストのCPU・メモリを採る間隔。`100 * time.Millisecond`以上、`Duration / 2`以下にします。fgprofのサンプリング間隔は変更しません。                                                                                            |
| `OnComplete`  | `func(hokaku.Result)`        | `nil`（通知なし）                   | 計測・送信処理が終了したときにワーカーから呼ばれます。`Result`の`ID`・`Directory`・`Err`で保存先と成否を確認できます。`Start`が同期的に返すエラーや、手動の`Upload`では呼ばれません。この中から`Close`を呼ばないでください。 |
| `HTTPClient`  | `*http.Client`                | `nil`（送信全体の時間制限なし） | 送信用client。指定するとコピーして使い、`Timeout == 0`は時間制限なし、正の値は指定した制限を維持します。接続確立のタイムアウトと`Close`・`Upload`のcontextによる中断は有効です。`CheckRedirect`は指定内容にかかわらずredirect拒否へ置き換えます。                                                                                |

必須項目の不足、URL・名前・計測時間の制約違反は`New`でエラーになります。ログの存在や権限、保存先への書き込み、ローテーションコマンドの実行可否は`Start`以降に確認されます。`Duration`と`Interval`は、`0`を既定値へ置き換えた後の組み合わせで検証されます。

両方のログパスを空にしても`Rotate`は必須です。切り替えるログがない場合は、`context`をimportして`Rotate: func(context.Context) error { return nil }`を指定できます。独自のコールバックではcontextのキャンセル・期限を処理してください。

計測時間を変更する場合は、アプリで`time`をimportし、上の設定例に`Duration: 120 * time.Second`などを追加します。中央サーバー側の保存先や容量の環境変数は[保存と送信](#保存と送信)を参照してください。

## ログ切り替えの準備

専用ログの出力設定と読み取り権限を用意し、ログ切り替え用コマンドをroot所有のヘルパーとして設定してください。NGINXはJSON形式、MySQLはFILE形式のslow logを使います。コマンドやサービス名は導入先に合わせて指定します。`Rotate`は固定コマンドまたは独自のコールバックで指定します。**リネームだけでなく、NGINXとMySQLが新しいファイルへ書くようreopenするところまで**がコールバックの責務です。失敗時には計測を開始しません。

ログパスを空にすると、そのログは空のartifactとして送ります。該当サービスがないホストでは、ヘルパーの対象サービスも外してください。アプリユーザーにsudo全般の権限を与える必要はありません。

ログは60秒の終了時点でファイルへ書き込まれたサイズまでをコピーします。終了時点でまだ応答・SQL処理が続いている場合、その後に書かれるログは含みません。NGINXのログbufferを有効にしている場合も末尾が未反映になり得るので、専用ログではbufferを指定しない構成にします。遅い処理の終了まで含めたい場合はDurationに余裕を持たせてください。計測中の別ローテーション・縮小・既存部分の書き換えを検出した場合は失敗します。copytruncateは競合による取りこぼしを完全には検出できないため、計測区間で実行しないでください。最後の行やSQLが未完了なら中央の解析でも拒否します。

## 保存と送信

ローカルは`OutputDir/<capture ID>/`、中央は既定で`data/captures/<capture ID>/<host>/`へ保存します。Dockerでは同じnamed volume内の`/data/captures/`です。中央の保存先は`KOSAKANA_CAPTURES_DIR`で変更できます。

| ファイル        | 内容                                                 |
| --------------- | ---------------------------------------------------- |
| `host.jsonl`    | gopsutilのCPU累積秒・メモリ総量/利用可能量・観測時刻 |
| `access.log`    | 切り替え後のNGINX JSONログ                           |
| `slow.log`      | 切り替え後のMySQL FILE slow log                      |
| `fgprof.pprof`  | このGoプロセスの待ち時間を含むprofile                |
| `manifest.json` | 環境、ホスト、開始終了時刻、ファイルサイズとSHA-256、任意のGit情報 |

送信先は`PUT /api/captures/<capture ID>/<host>/<file>`です。Bearer tokenで認証し、manifestは最後に送ります。中央はファイルのサイズ・SHA-256・入力形式を確認し、共通のファイル解析処理でHTTP/SQL/CPU/メモリ/profileを解析します。profile変換失敗などは原本を残して部分取得と診断を表示します。推定遷移は中央設定のセッションCookie名を使い、NGINXログにCookie・User-Agentがある場合に解析します。到着済みホストの結果を1つのベンチとして一覧へ登録し、fgprofもProfile画面で表示します。CPU pprofのCPU時間と混同しないよう、fgprofには待ち時間を含むことを表示します。取得済みprofileは`go tool pprof -http=127.0.0.1:6061 fgprof.pprof`で解析できます。

HTTPSまたはloopbackへのSSHトンネルを使います。HTTPの外部アドレスやredirectへの送信は拒否します。ネットワークエラーと5xxは最大3回試行し、4xxはその場で失敗します。ローカルファイルは成功後も自動削除しません。送信が失敗した完了記録は、同じRecorderの`Upload(ctx, directory)`で再送できます。再送先のdirectoryは`OutputDir`直下の完了captureに限定し、ディレクトリ・manifest・artifactのsymlinkは拒否します。正常な再送は二重計数しません。

hokaku側にはファイル・ホスト単位の容量上限はありません。受信側の `KOSAKANA_CAPTURE_MAX_FILE_BYTES`・`KOSAKANA_CAPTURE_MAX_HOST_BYTES`・`KOSAKANA_CAPTURE_MAX_BYTES` とリバースプロキシのリクエストサイズ上限は別に適用されます。送信サイズ以上に設定してください。大きなログはローカルと受信先のディスク容量を消費します。生ログは24時間で自動削除されません。必要なprofileを退避したうえで、中央アプリを停止して保存先を整理してください。再開時に容量を数え直します。DBの計測値は従来どおり24時間保持です。

CPU使用率は累積秒の差分から計算し、初回・カウンターリセット・長い欠測区間は欠測にします。メモリ使用率は`1 - available / total`です。SQLはslow logの`SET timestamp`（欠けている場合は`# Time:`）、HTTPはJSONの`timestamp`を使い、回収時刻へ置き換えません。

## 複数サーバーと計測区間

環境名を揃え、ホスト名は各サーバーで変えます。Startを呼んだGoプロセスと、そのホストのログ・メトリクスを採ります。複数ホストを1つのcaptureにまとめる場合は、開始通知を行う側で共通IDを決め、各アプリで`recorder.StartWithID("bench-001")`を呼びます。中央は同じIDの各ホストをまとめて保持・取り込みし、既存のベンチsnapshotを更新します。後から別ホストが届いた場合や再送した場合も、編集済みのスコア・PASS/FAIL・メモは維持します。IDは毎回変え、同じローカルディレクトリを再利用しません。中央と各ホストの時計はNTPで同期してください。共通IDは時計ずれの補正や、予定した全ホストの到着保証を行うものではありません。別サーバーへの開始通知そのものを自動配信する機能はないため、各アプリへ同じIDを渡す連携が必要です。`Start()`だけを各ホストで呼ぶとそれぞれ別IDのcaptureになります。

既定の60秒はStartからの時間です。ISUCON14の負荷フェーズはinitialize後の追加準備より後に始まるため、負荷フェーズ全体を厳密に覆う保証はありません。`Duration`で240秒まで変更できます。初期化呼び出しと実負荷区間の違いを残して比較してください。

## 検証

```sh
go test -race ./...
go vet ./...
```

中央も含む結合検証は、別のkosakanaリポジトリで`npm run record:check`を実行します。3ホスト分の合成HTTP/SQLログ、実gopsutil、実fgprofを使って、送信・再送・集計を確認します。実ISUCONベンチの得点や計測負荷はこのテストでは検証しません。

参考: [fgprof](https://github.com/felixge/fgprof)、[gopsutil](https://github.com/shirou/gopsutil)。

## プロセス別CPU・メモリ

Linuxでは既定でプロセスのCPU累積時間・RSS・PID・開始時刻・実行名を収集し、`host.jsonl`に同梱します。
対応するkosakanaで、mysqld・nginx・アプリなどの実行名ごとの合計とPID別内訳を確認できます。
先に受信側を更新してください。[形式・欠測・負荷の制御](docs/process-metrics.md)、[負荷評価](docs/overhead.md)を参照してください。
