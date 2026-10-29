# hokakuの計測負荷評価

## 再現方法

`cmd/overhead`は別プロセスのGo HTTPサーバーへ、16クライアントで継続リクエストを送ります。
各リクエストでSHA-256を2,000回計算します。`-mixed`はさらに2msの待機を入れます。
各試行は100リクエストのwarmup後、10秒計測。試行ごとにサーバープロセスを作り直し、
順序を巡回して5回ずつ測ります。

比較する4条件は次のとおりです。

- `none`: hokaku/fgprofなし。
- `fgprof`: fgprofのみ。
- `host`: fgprof + 従来のホストCPU/メモリ計測（`DisableProcessMetrics: true`）。
- `process`: 上記 + 今回のプロセス計測（既定1秒）。

`host`/`process`では実際のRecorder、JSONL書き込み、manifest作成、loopback HTTPへの送信まで実行します。
HTTP性能・CPU/RSSの測定区間は最初の10秒、Recorderは11秒間動作するため、終了時のハッシュ・送信は
負荷測定区間の外です。NGINXアクセスログやMySQL slow logはこの実験では空です。
本番ログのローテーション、書き込み、コピー、ネットワーク送信の負荷を評価したものではありません。

環境: 2026-10-29、Docker Desktop Linux/arm64、Go 1.26.10。
公式イメージ`golang:1.26-bookworm`（digest `sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb`）。
コンテナをCPU 0,1へ固定し、`GOMAXPROCS=1`。CPU quotaは付けません。
100個のsleepプロセスを追加して約103プロセスが見える環境で実行しました。
CPU負荷条件に加え、待機goroutineが多いfgprofへの影響を見るため、mixed条件では1,000個の待機goroutineを追加します。
最終測定中はこの作業のビルド・テストを並行実行していません。

```sh
go build -o overhead ./cmd/overhead
GOMAXPROCS=1 ./overhead -seconds 10 -repeats 5 -workers 16 -out overhead-results-cpu
GOMAXPROCS=1 ./overhead -seconds 10 -repeats 5 -workers 16 -mixed -parked 1000 -out overhead-results-mixed
```

今回のLinux環境の再現例（リポジトリを`/src`へmountしLinux用バイナリをビルドしてから実行）:

```sh
docker run --rm --cpuset-cpus 0,1 -e GOMAXPROCS=1 -v "$PWD:/src" -w /src \
  golang:1.26-bookworm sh -c '
    i=0
    while [ "$i" -lt 100 ]; do sleep 900 & i=$((i+1)); done
    ./overhead -seconds 10 -repeats 5 -workers 16 -out overhead-results-cpu
    ./overhead -seconds 10 -repeats 5 -workers 16 -mixed -parked 1000 -out overhead-results-mixed
  '
```

出力の`server_cpu_seconds`は負荷区間のサーバープロセス自身のuser+system CPU秒で、クライアントを含みません。
`server_rss_kib`は負荷終了時のRSSです。`max_rss_kib`はOSのhigh-water値も残しますが、fork/exec前の
親のメモリ履歴を含む場合があるため、比較表には使用しません。
fgprofのprofileは各試行で保存し、pprofとしてparseしてworkloadとprocess.scanを含むサンプルを数えます。

## 走査・エンコード単体のスケーリング

Linux上に合成したproc statファイルを100/1,000/2,048個作り、走査とJSON化を測定しました。
ウォームな通常ファイルを使う単体ベンチであり、実procfs・CPU競合・cold cacheの上限保証ではありません。
このベンチは全件を処理しています。現行実装も25msの経過時間制限を撤去し、最大2,048 PIDの試行とcontextキャンセルで制御します。

```sh
go test ./internal/process -run '^$' -bench BenchmarkScanAndEncode -benchtime=1s -count=3
```

| PID数 | 中央値/走査+JSON化 | 割当量/走査（約） | JSON/走査（約） | 1秒間隔で60秒のJSON量 |
| --- | ---: | ---: | ---: | ---: |
| 100 | 0.498 ms | 163 KiB | 8 KiB | 0.47 MiB |
| 1,000 | 6.291 ms | 1.60 MiB | 80 KiB | 4.69 MiB |
| 2,048 | 11.733 ms | 3.47 MiB | 165 KiB | 9.67 MiB |

割当量は保持メモリではありません。プロセス数が多いとGCの仕事が増えます。
件数・時間予算に到達すると、上位プロセスを推測せず`partial`と`truncated`を送ります。

## 適用範囲

これはLinux VM上の合成Go HTTPアプリの比較です。実ISUCONのスコア、実際のMySQL/NGINX負荷、
別CPUアーキテクチャ、競技サーバーでのtail latencyを保証しません。
CPU固定も物理ホストの独占を意味しません。数%の差には環境ノイズが含まれ得るため、生データと範囲を併記します。
最終スコアの確認は計測なしの実ベンチと比較してください。多数PIDや許容できない劣化が出た環境では、
`ProcessInterval`を2〜10秒へ延ばすか`DisableProcessMetrics`で切り分けられます。

## HTTP負荷試験の結果

数値は5試行の中央値。p95は各試行のp95の中央値です。HTTPエラーは全40試行で0件でした。

### CPU中心

| 条件 | req/s | req/s範囲 | p95 (ms) | アプリCPU秒 / 約10秒 | 終了時RSS (MiB) |
| --- | ---: | ---: | ---: | ---: | ---: |
| none | 3754.1 | 3376.4–3804.8 | 7.744 | 9.984 | 16.15 |
| fgprof | 3595.2 | 3456.1–3776.9 | 7.480 | 9.458 | 14.40 |
| host | 3642.6 | 3603.7–3724.0 | 7.651 | 9.775 | 14.41 |
| process | 3633.7 | 3575.5–3725.9 | 7.712 | 9.950 | 16.49 |

追加プロセス収集のreq/s変化（process / host − 1）は **-0.24%**、p95は **+0.79%**。
計測全体（process / none − 1）はreq/s **-3.21%**、p95 **-0.41%**でした。
実procfs走査は55回、中央値 **0.801ms**、最大 **6.938ms**。
不完全走査は0回。
生データ: [cpu.jsonl](performance/cpu.jsonl)、[走査時間](performance/cpu-scans.json)。

### CPU + 2ms待機 + 1,000待機goroutine

| 条件 | req/s | req/s範囲 | p95 (ms) | アプリCPU秒 / 約10秒 | 終了時RSS (MiB) |
| --- | ---: | ---: | ---: | ---: | ---: |
| none | 3674.9 | 3593.3–3776.0 | 5.851 | 9.726 | 16.21 |
| fgprof | 3359.9 | 3201.0–3454.8 | 7.785 | 9.850 | 16.36 |
| host | 3421.0 | 3186.8–3431.6 | 7.414 | 9.983 | 18.29 |
| process | 3348.9 | 3239.8–3414.9 | 7.485 | 9.918 | 16.57 |

追加プロセス収集のreq/s変化（process / host − 1）は **-2.11%**、p95は **+0.95%**。
計測全体（process / none − 1）はreq/s **-8.87%**、p95 **+27.94%**でした。
実procfs走査は55回、中央値 **0.893ms**、最大 **3.517ms**。
不完全走査は0回。
生データ: [mixed.jsonl](performance/mixed.jsonl)、[走査時間](performance/mixed-scans.json)。

### 判定とfgprofへの影響

この条件では、追加したプロセス収集単独が大きなボトルネックになる兆候はありませんでした。
ただし追加時のmixed req/sは約2%低下しており、影響ゼロとは扱えません。
プロセス数・CPU余力・GC状態によって変わるので、件数上限も性能劣化率の保証ではありません。

**既存のfgprofを含む計測全体の影響は無視できません。** 1,000待機goroutineを置いたmixedでは、
計測なし比でreq/sが約9%低下し、p95が約28%増えました。
fgprofだけでもreq/sが約8.6%低下しており、追加走査だけに原因を帰属できません。
細かな改善をスコアで比較する場合、計測あり同士で条件を揃え、最終確認は計測なしでも行う必要があります。

全profileはpprofとして読み取れました。収集goroutineはfgprofの対象に含まれます。
短い走査はサンプリングに現れない試行もあり、`profile_scan_ns=0`は負荷ゼロを意味しません。
また**mixedのworkload関数はfgprof-onlyを含む全試行でサンプル0**、CPU条件でも0の試行がありました。
今回の単一Go実行スレッド・短いCPU処理では、fgprofの関数別帰属をCPU時間の正確な内訳として検証できません。
これは追加走査の有無に共通する観測であり、プロセス別CPUカウンターとは区別してください。
待機goroutineの時間も分母に入るため、`profile_scan_ns / profile_total_ns`をCPU占有率としては示しません。

単体走査ベンチの生出力: [scan-benchmark.txt](performance/scan-benchmark.txt)。


## 25ms制限撤去時の確認（2026-10-29）

Linux amd64 / Ryzen Threadripper 7960X、テスト用statファイルを用いた`BenchmarkScanAndEncode`、
`-test.benchtime=100ms`では100 PIDで0.89ms、1,000 PIDで9.18ms、2,048 PIDで18.91msでした。
実際のprocfsやCPU飽和下の値・性能保証ではありません。別の回帰テストでは最初の読み取りに35msの待機を
挟んでも全3 PIDを取得し、キャンセル時には取得済み値を保持して次の読み取り前に停止することを確認しています。
