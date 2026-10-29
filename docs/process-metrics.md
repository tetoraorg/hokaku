# プロセス別CPU・メモリ

Linuxでは既定で1秒ごとに、見えるPIDの`/proc/<pid>/stat`を読みます。
`host.jsonl`の任意フィールド`processes`へ同梱し、既存のmanifest・SHA-256・再送処理で送信します。
**先にkosakanaの対応版を導入してください。** 旧受信側のstrict schemaは追加フィールドを拒否します。
旧hokakuのデータは新受信側でも引き続き利用できます。

- `pid` + `start_ticks`でプロセスを識別します。start_ticksは桁落ちしない文字列です。
- `name`はLinuxのcommです。通常15バイトまでで、コマンドラインではありません。
  同名のnginx workerはまとめられます。MySQLは通常`mysqld`、MariaDBは`mariadbd`、
  アプリは実行名で表示されます。長い名前の衝突・プログラム自身による名前変更はあり得ます。
- `cpu_seconds`はutime + stimeの累積秒。子プロセスの時間を加えず、スレッドも別途列挙しません。
  中央で同じプロセスの差分 / 観測間隔 × 100を計算します。1論理コア=100%で、100%を超えます。
  ホストCPU使用率（全コア=100%）とは尺度が異なります。
- `rss_bytes`はその時点のRSS。共有ページは複数プロセスで重複計上されます。
  PSS/USSやファイルキャッシュ込みのホスト使用メモリとは異なります。
- 初回、PID再利用、名前変更、CPUカウンター減少、30秒を超える間隔ではCPUは欠測です。
  走査間に起動・終了した短命プロセスの全CPU時間は回収できません。

## 負荷の制御と欠測

`Config.ProcessInterval`は1〜10秒、既定1秒です。実際の走査は`Config.Interval`のホスト計測tickに乗るので、
ホスト間隔が長い場合はそれ以上の頻度にはなりません。`Config.DisableProcessMetrics: true`で無効化できます。
CLIでは`--process-interval 2s`、`--disable-process-metrics`です。

各走査は最大2,048 PIDの読み取り試行で制限し、読み取り間でcontextのキャンセルを確認します。
旧25msの経過時間制限は撤去しました。CPU競合によるスケジューラ待ちで後半のPIDを繰り返し捨てないためです。
進行中のprocfs読み取りは強制中断できず、大量のPIDや高負荷では走査がホスト計測tickを遅らせることがあります。
`duration_ms`で実時間、`skipped`で読み取り失敗数、`truncated`でキャンセル・件数上限を記録します。
上限時のPID集合はCPU上位順ではなくprocfs列挙順です。並列走査・追いつくための追加走査は行いません。

`status`は`ok`、`partial`、`unavailable`のいずれかです。プロセス終了、権限不足、上限到達などで
プロセス走査が不完全でもホスト計測・fgprof・送信は継続します。kosakanaはpartialの名前別合計を
完全な使用量として表示せず、取得できたPID別値と欠測理由を示します。

macOSなどLinux以外は`unavailable`です。コンテナではそのPID namespaceに見えるプロセスだけを採ります。
`hidepid`などでPIDの存在自体が隠される場合、隠されたPID数は検出できません。
ホストのMySQL/NGINXまで採るには、ホスト上の実行ユーザーから対象`/proc`が読める構成が必要です。
コマンドライン・環境変数は送信せず、負荷の高い`smaps`走査やシェル起動も行いません。

fgprofは計測を組み込んだGoプロセスのgoroutineを観測するため、収集処理と待機中の収集goroutineもprofileに含まれます。
待機を含むfgprofの割合をCPU占有率と読み替えないでください。ログのコピー・ハッシュ・アップロードは従来同様、
計測終了後に行います。負荷フェーズが設定Durationを超えると、それらが負荷と重なる可能性があります。

測定方法と実測値は[負荷評価](overhead.md)を参照してください。

## 収集方法の参考

[OpenTelemetry Collector process scraper](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/receiver/hostmetricsreceiver/internal/scraper/processscraper/process_scraper.go)
はプロセスごとの累積CPU時間を取得し、部分的なエラーでも成功したメトリクスを保持します。
hokakuもこの方式を継続します。全プロセスへの多数の属性問い合わせは避け、1 PIDにつきstatの1回の読み取りで
CPU時間・RSS・起動時刻を得ます。件数制限は残るため、2,048 PIDを超える環境ではpartialになります。
