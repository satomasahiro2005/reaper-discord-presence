# REAPER → Discord Rich Presence

REAPER を開いている間、Discord のステータスに「REAPER で作業中」を出すツール。Node.js もユーザートークンも使わず、Go の単体 exe ひとつと REAPER の Lua スクリプト 1 本で動く。Windows 専用。

![license](https://img.shields.io/github/license/satomasahiro2005/reaper-discord-presence)
![release](https://img.shields.io/github/v/release/satomasahiro2005/reaper-discord-presence)
![platform](https://img.shields.io/badge/platform-Windows-blue)

## 特徴

- 常駐は exe ひとつだけ。Node も常駐サーバもいらない。
- 公式の Rich Presence IPC を使う。ユーザートークンや self-bot は使わない。
- 選択トラックの先頭 FX 名を出す。よく使う VST はアイコンと配布ページへのボタンを登録できる。
- しばらく操作も発音もないと「離席中」に切り替わり、また弾けば戻る。
- 表示する行の中身は config（JSON）で自由に組み替えられる。
- プロジェクトのファイル名は送らない。

## 表示例

```
Playing REAPER
v7.74 · 48kHz · 128spls · 2.2/3.0ms
▶️ Serum · 128 BPM
```

各行のテンプレートは config で変えられる（→ [設定リファレンス](#設定リファレンス)）。これに REAPER アイコン（または登録 VST のアイコン）・経過時間・ボタンが付く。1 行目の「REAPER」は Discord の App 名なので、Developer Portal で付けた名前がそのまま出る。

## 仕組み

Lua は REAPER の状態を JSON に書き出すだけ。Discord との通信は exe がやる。両者は JSON ファイル 1 つでつながっている。

```
REAPER 起動
└─ Scripts/__startup.lua
   └─ reaper_discord_presence.lua    状態を JSON に書き出し、exe を起動・監視
      └─ %APPDATA%\REAPER\reaper_discord_presence.json
         └─ reaper-discord-presence.exe    JSON を読んで Discord のローカル IPC へ送る
```

Rich Presence はデスクトップ版 Discord のローカル IPC に送る方式なので、送信役の常駐プロセスがどうしても要る。それを最小限の Go exe にまとめてある。exe が落ちても Lua 側が気づいて起動し直すので、REAPER を開いている限り勝手に復帰する。JSON の更新が一定時間途絶えたら REAPER が閉じたとみなして表示を消す。

## 必要なもの

- Windows 10 / 11
- Discord デスクトップ版（ブラウザ版は IPC 非対応）
- REAPER
- Discord Developer Portal の Application 1 つ

## セットアップ

### 1. Discord Application を作る

[Discord Developer Portal](https://discord.com/developers/applications) で New Application を作り、名前を `REAPER` にする。この App 名がそのまま「Playing REAPER」の "REAPER" 部分になる。**General Information** の **Application ID** を控えておく。

### 2. 画像を登録する

**Rich Presence → Art Assets** に画像をアップロードする。

- 大画像: キー `reaper`（小文字）で REAPER アイコン。
- 状態バッジ（任意）: キー `play` / `pause` / `record` / `stop` の 4 枚。登録すると再生状態に応じた小バッジが付く。無くても動く（バッジが出ないだけ）。

512×512 以上の PNG 推奨。反映に数分かかることがある。

### 3. exe を用意する

- ダウンロード: [Releases](../../releases) の `reaper-discord-presence.exe`。
- 自分でビルド: [Go](https://go.dev/dl/) を入れて `./build.ps1`。コンソール窓の出ない静的な単体 exe ができる。

### 4. 配置する

次の 2 ファイルを REAPER の Scripts フォルダに置く。

```
%APPDATA%\REAPER\Scripts\reaper-discord-presence.exe
%APPDATA%\REAPER\Scripts\reaper_discord_presence.lua
```

`%APPDATA%\REAPER\Scripts\__startup.lua` の末尾に次の 1 行を足す（既存の内容は消さない）。

```lua
dofile(reaper.GetResourcePath() .. "/Scripts/reaper_discord_presence.lua")
```

### 5. 設定する

初回起動で `%APPDATA%\REAPER\reaper_discord_presence_config.json` が自動生成される。`clientId` を手順 1 の Application ID に書き換える。各項目は[設定リファレンス](#設定リファレンス)を参照。

### 6. 確認する

Discord デスクトップ版と REAPER を起動する。数秒で `Playing REAPER` が出る。再生・停止・録音やテンポ変更で表示が変わり、REAPER を閉じると数秒で消える。

## 設定リファレンス

| キー | 既定値 | 説明 |
|------|--------|------|
| `clientId` | — | Discord Application ID（必須） |
| `largeImageKey` | `reaper` | 大画像の Art Asset キー |
| `largeImageText` | `""` | 大画像のキャプション（テンプレート可、例 `REAPER v{ver}`）。空ならタイトルバー文字列。`listening` では行として見える |
| `activityType` | `playing` | 1 行目の動詞: `playing` / `listening`（→ Listening to）/ `watching` / `competing`。RPC はこの 4 つだけ |
| `pollIntervalMs` | `2000` | 状態 JSON を確認する間隔（ミリ秒） |
| `staleAfterMs` | `60000` | JSON がこの時間更新されないと REAPER 終了とみなす。VST 読込中などの一時停止で消えないよう既定 60 秒。正常終了は即消える（Lua が状態ファイルを消すため） |
| `awayAfterMs` | `600000` | この時間 操作も発音もないと「離席中」に切替（10 分）。`0` で無効 |
| `awayText` | `Idle` | 離席中に出す文言（例 `Idle` / `離席中`） |
| `awayImageKey` | `""` | 離席中の大画像キー。空なら `largeImageKey` と同じ |
| `resetTimerOnAway` | `true` | 経過時間の挙動。`true`: 離席中は離席時間を出し、戻ると 0 から。`false`: 離席をまたいでセッション開始からの通算を出し続ける |
| `detailsFormat` | `v{ver} · {srate} · {bufsize} · {latency}` | 2 行目のテンプレート（下記） |
| `stateFormat` | `{emoji} {fxOrTransport} · {bpm}` | 3 行目のテンプレート（下記） |
| `showElapsed` | `true` | 経過時間を表示する |
| `smallImageByTransport` | `true` | 再生状態の小バッジを出す（要 `play`/`pause`/`record`/`stop`） |
| `swapImages` | `false` | 大画像と小バッジを入れ替える（VST/状態アイコンを大きく、REAPER を小バッジに） |
| `vsts` | `[]` | プラグイン登録表（下記） |
| `button1Label` / `button1Url` | `Get REAPER` / reaper.fm | ボタン 1。空で非表示 |
| `button2Label` / `button2Url` | `""` | ボタン 2。空で非表示 |

ボタンは Discord の仕様で自分には出ず、プロフィールを見た他人にだけ出る（アイコンや各行は自分にも見える）。プロジェクトのファイル名は送らない。

### 表示テンプレート（`detailsFormat` / `stateFormat`）

2・3 行目の中身は config のテンプレート文字列で決まる。使えるプレースホルダ:

| プレースホルダ | 中身 |
|------|------|
| `{title}` | タイトルバー文字列（例 `REAPER v7.74 -Licensed ...`）。ウィンドウタイトルを読む方式 |
| `{version}` | バージョン（arch 付き、例 `7.74/x64`） |
| `{ver}` | バージョン（arch 無し、例 `7.74`） |
| `{emoji}` | 再生状態の絵文字（▶️ / ⏸️ / ⏺️ / ⏹️） |
| `{transport}` | 再生状態の語（Playing / Paused / Recording / Stopped） |
| `{fx}` | 選択トラックの先頭 FX 名（登録 VST なら登録名、無ければ空） |
| `{fxOrTransport}` | `{fx}` があればそれ、無ければ `{transport}` |
| `{bpm}` | テンポ（例 `128 BPM`、無ければ空） |
| `{srate}` | サンプルレート（例 `48kHz`） |
| `{bufsize}` | ブロックサイズ（例 `128spls`）。ReaScript に API が無いので `reaper.ini` から読む＝環境設定を保存した時点で更新される（実行中の変更は即時反映されない） |
| `{latency}` | 入出力レイテンシ（例 `2.2/3.0ms`） |
| `{bps}` | ビット深度（例 `24bit`） |
| `{channels}` | 入出力チャンネル数（例 `2/2ch`） |
| `{driver}` | オーディオドライバ（例 `ASIO`） |

空のプレースホルダは消える。区切りに `·`（中黒）を使うと、空セグメント前後の `·` も自動でまとまる（例: テンポ無しの `{emoji} {fxOrTransport} · {bpm}` → `▶️ Serum`）。`·` 以外の区切りは自動では消えない。

例:
- `"stateFormat": "{transport} · {bpm}"` → `Playing · 128 BPM`
- `"stateFormat": "{fx}"` → `Serum`
- `"detailsFormat": "REAPER {version}"` → `REAPER 7.74/x64`

### プラグイン登録（`vsts`）

選択トラックの先頭 FX が登録名を含むとき、専用アイコン（小バッジ）と配布ページへのボタンを出せる。

```json
"vsts": [
  { "match": "Serum", "label": "Serum", "imageKey": "serum", "downloadUrl": "https://xferrecords.com/products/serum" }
]
```

| フィールド | 説明 |
|------|------|
| `match` | FX 名に含まれていれば一致（大小無視） |
| `label` | 表示名 |
| `imageKey` | 小バッジの Art Asset キー（その VST のロゴを別途アップロード）。省略可 |
| `downloadUrl` | ボタン 2 を「Get &lt;label&gt;」としてこの URL に。省略可 |

一致時は小バッジが登録アイコン優先（無ければ再生状態バッジ）、ボタン 2 がその VST の配布リンクになる。

## うまく動かないとき

**表示が出ない**
- Discord デスクトップ版が起動しているか（ブラウザ版は不可）。
- `clientId` が合っているか。
- 他人に見せたいなら Discord の **設定 → アクティビティのプライバシー →「現在のアクティビティを…ステータスとして表示」** を ON に。
- ログ `%APPDATA%\REAPER\reaper_discord_presence.log` を見る。

**画像が出ない / 既定画像になる**
- Art Asset のキーが `reaper`（小文字）か確認。アップロード直後は反映に数分かかる。

**勝手に「離席中」になる / 戻らない**
- しばらく操作も発音もないと離席中になるのは仕様（`awayAfterMs`）。無効にするなら `0`、長くするなら値を大きく。弾くか操作すれば戻る。
- REAPER 終了後に消えないときは `staleAfterMs` を短く。

## ログ

GUI ビルドなのでコンソール出力は無い。代わりに `%APPDATA%\REAPER\reaper_discord_presence.log` に書き出す。

## ライセンス

[MIT](LICENSE)
