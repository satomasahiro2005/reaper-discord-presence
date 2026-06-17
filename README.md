# REAPER → Discord Rich Presence

REAPER を起動している間、Discord のプロフィールに「いま REAPER で作業中」というステータスを表示するツールです。Node.js もユーザートークンも使わず、**Go 製の単体 `.exe` ひとつ**と小さな **REAPER Lua スクリプト**だけで動きます。

![license](https://img.shields.io/github/license/satomasahiro2005/reaper-discord-presence)
![release](https://img.shields.io/github/v/release/satomasahiro2005/reaper-discord-presence)
![platform](https://img.shields.io/badge/platform-Windows-blue)

## 特長

- **Node 不要。** 配布物は単体 exe と Lua スクリプトだけ。常駐するのは exe ひとつです。
- **公式の Rich Presence IPC のみ。** ユーザートークンや self-bot は一切使いません。
- **バージョンは公式APIから取得。** 既定では `reaper.GetAppVersion()` でバージョンを取得します（ウィンドウ走査に依存しない安全な方法）。ライセンス表記つきのタイトルバー文字列を出したい場合だけ `detailsFormat` を `{title}` にできます。
- **起動も終了も自動。** REAPER を起動すれば表示され、閉じれば数秒で消えます。一定時間操作がなければ「離席中」表示に切り替わり、戻ると経過時間が 0 からリスタートします。
- **選択トラックのプラグインを表示。** 選択中トラックの先頭 FX 名（例: Serum）を出します。よく使う VST は専用アイコンや配布ページへのボタンに登録できます。
- **プライバシー配慮。** プロジェクトのファイル名は送信も表示もしません。

## 表示例

```
Playing REAPER
REAPER v7.74/x64
▶️ Serum · 128 BPM
```

3 行目は「再生状態の絵文字 + 選択トラックの先頭 FX（無ければ再生状態の語）+ テンポ」です（▶️ Playing / ⏸️ Paused / ⏺️ Recording / ⏹️ Stopped）。これに REAPER アイコン（または登録 VST のアイコン）・経過時間・ボタンが付きます。

## 仕組み

REAPER 側（Lua）は状態を JSON に書き出すだけで、Discord との通信は exe が担当します。両者は 1 つの JSON ファイルを介して疎結合になっています。

```
REAPER 起動
└─ Scripts/__startup.lua
   └─ reaper_discord_presence.lua      … 2 秒ごとに状態を JSON へ書き出し、exe を 1 度だけ起動
      └─ %APPDATA%\REAPER\reaper_discord_presence.json
         └─ reaper-discord-presence.exe … JSON を監視し、Discord のローカル IPC へ送信
```

Discord の Rich Presence は、デスクトップ版クライアントのローカル IPC へ送る仕組みです。そのため送信役の常駐プロセスがどうしても必要になりますが、本ツールではそれを最小限の Go 製 exe に収めています。

JSON ファイルの更新時刻（mtime）を生存確認に使い、一定時間更新が途絶えたら「REAPER が終了した」とみなして表示を消します。

## 必要なもの

- Windows 10 / 11
- Discord デスクトップ版（ブラウザ版は IPC 非対応）
- REAPER
- Discord Developer Portal で作成した Application（1 つ）

## セットアップ

### 1. Discord Application を作る

[Discord Developer Portal](https://discord.com/developers/applications) で **New Application** を作り、名前を `REAPER` にします。この **App 名がそのまま「Playing REAPER」の "REAPER" 部分**になります。作成後、**General Information** にある **Application ID** を控えておきます。

### 2. 画像を登録する

**Rich Presence → Art Assets** で画像をアップロードします。

- **大画像**: キーを `reaper`（小文字）にして REAPER アイコンを登録します。
- **状態バッジ（任意）**: キー `play` / `pause` / `record` / `stop` の 4 枚を登録すると、再生状態に応じた小さなバッジが付きます。登録しなくても問題ありません（バッジが出ないだけ）。

いずれも 512×512 以上の PNG を推奨します。アップロードの反映には数分かかることがあります。

### 3. exe を入手する

- **ダウンロード:** [Releases](../../releases) から `reaper-discord-presence.exe` を入手します。
- **自分でビルド:** [Go](https://go.dev/dl/) を入れて `./build.ps1` を実行します。コンソール窓の出ない、完全静的な単体 exe が生成されます。

### 4. 配置する

次の 2 ファイルを REAPER の Scripts フォルダにコピーします。

```
%APPDATA%\REAPER\Scripts\reaper-discord-presence.exe
%APPDATA%\REAPER\Scripts\reaper_discord_presence.lua
```

続いて `%APPDATA%\REAPER\Scripts\__startup.lua` の末尾に次の 1 行を追記します（既存の内容は消さないでください）。

```lua
dofile(reaper.GetResourcePath() .. "/Scripts/reaper_discord_presence.lua")
```

### 5. 設定する

初回起動時に `%APPDATA%\REAPER\reaper_discord_presence_config.json` が自動生成されます。`clientId` を手順 1 の Application ID に書き換えてください。各項目は[設定リファレンス](#設定リファレンス)を参照。

### 6. 動作を確認する

Discord デスクトップ版と REAPER を起動します。数秒後、Discord のプロフィールに `Playing REAPER` が表示されます。再生・停止・録音やテンポの変更で 3 行目が変わり、REAPER を閉じると数秒で表示が消えます。

## 設定リファレンス

| キー | 既定値 | 説明 |
|------|--------|------|
| `clientId` | — | Discord Application ID（**必須**） |
| `largeImageKey` | `reaper` | 大画像の Art Asset キー |
| `largeImageText` | `""` | 画像ホバー時の文字。空ならタイトルバー文字列を自動使用 |
| `pollIntervalMs` | `2000` | 状態 JSON を確認する間隔（ミリ秒） |
| `staleAfterMs` | `60000` | JSON がこの時間更新されないと REAPER 終了とみなす（ミリ秒）。VST 読込等で一時的に応答なしになっても消えないよう既定 60 秒。正常終了は即クリア（Lua の atexit が状態ファイルを消すため） |
| `awayAfterMs` | `600000` | この時間 REAPER を操作しないと「離席中」表示に切替（10 分）。`0` で無効。戻ると経過時間が 0 にリセット |
| `awayText` | `Idle` | 離席中に 3 行目へ出す文言（例 `Idle` / `離席中`） |
| `awayImageKey` | `""` | 離席中の大画像 Art Asset キー。空なら `largeImageKey` と同じ |
| `detailsFormat` | `v{ver} · {srate} · {bufsize} · {latency}` | 2 行目のテンプレート（下記） |
| `stateFormat` | `{emoji} {fxOrTransport} · {bpm}` | 3 行目のテンプレート（下記） |
| `showElapsed` | `true` | 経過時間（for HH:MM）を表示する |
| `smallImageByTransport` | `true` | 再生状態の小バッジを表示する（要 `play`/`pause`/`record`/`stop` アセット） |
| `swapImages` | `false` | 大画像と小バッジを入れ替える（VST/状態アイコンを大きく、REAPER を小バッジに） |
| `vsts` | `[]` | プラグイン登録表（下記参照） |
| `button1Label` / `button1Url` | `Get REAPER` / reaper.fm | ボタン 1。空にすると非表示 |
| `button2Label` / `button2Url` | `""` | ボタン 2。空にすると非表示 |

> ボタンは Discord の仕様上、**自分には表示されず、プロフィールを見た他人にだけ**表示されます（アイコンや 3 行目は自分にも見えます）。
> プロジェクトのファイル名は仕様として一切送信・表示しません。

### 表示テンプレート（`detailsFormat` / `stateFormat`）

2 行目・3 行目の中身は、`config.json` のテンプレート文字列を書き換えるだけで自由に変えられます。使えるプレースホルダ:

| プレースホルダ | 中身 |
|------|------|
| `{title}` | REAPER のタイトルバー文字列（例 `REAPER v7.74 -Licensed ...`）。ライセンス表記を含むが**ウィンドウタイトルを読む**方式 |
| `{version}` | バージョン（arch 付き、例 `7.74/x64`） |
| `{ver}` | バージョン（arch 無し、例 `7.74`）。`/x64` を出したくない時はこちら |
| `{emoji}` | 再生状態の絵文字（▶️ / ⏸️ / ⏺️ / ⏹️） |
| `{transport}` | 再生状態の語（Playing / Paused / Recording / Stopped） |
| `{fx}` | 選択トラックの先頭 FX 名（登録 VST なら登録名。無ければ空） |
| `{fxOrTransport}` | `{fx}` があればそれ、無ければ `{transport}` |
| `{bpm}` | テンポ（例 `128 BPM`。テンポ無しなら空） |
| `{srate}` | サンプルレート（例 `48kHz`。`reaper.GetSetProjectInfo` 由来） |
| `{bufsize}` | オーディオブロックサイズ（例 `128 spls`）。ReaScript APIが無いため `reaper.ini` のASIO設定から読む＝**環境設定を保存した時点で更新**（実行中の変更は即時反映されない） |
| `{latency}` | 入出力レイテンシ（例 `2.2/3.0ms`。`reaper.GetInputOutputLatency` 由来） |

値が空のプレースホルダは消えます。区切りに **`·`（中黒）** を使うと、空セグメントの前後の `·` も自動で消えてきれいに詰まります（例: テンポ無しの `{emoji} {fxOrTransport} · {bpm}` → `▶️ Serum`）。`·` 以外の区切りは自動では消えません。

例:
- `"stateFormat": "{transport} · {bpm}"` → `Playing · 128 BPM`
- `"stateFormat": "{fx}"` → `Serum`
- `"detailsFormat": "REAPER {version}"` → `REAPER 7.74/x64`

### プラグイン登録（`vsts`）

選択トラックの先頭 FX が登録した名前を含むとき、専用アイコン（小バッジ）と配布ページへのボタンを出せます。

```json
"vsts": [
  { "match": "Serum", "label": "Serum", "imageKey": "serum", "downloadUrl": "https://xferrecords.com/products/serum" }
]
```

| フィールド | 説明 |
|------|------|
| `match` | FX 名に含まれていれば一致（大文字小文字無視） |
| `label` | 3 行目に表示する名前 |
| `imageKey` | 小バッジに使う Art Asset キー（その VST のロゴを別途アップロード）。省略可 |
| `downloadUrl` | ボタン 2 を「Get &lt;label&gt;」としてこの URL に。省略可 |

一致したときは、小バッジは登録アイコン優先（無ければ再生状態バッジ）、ボタン 2 はその VST の配布リンクになります。

## トラブルシューティング

**表示が出ない**
- Discord デスクトップ版が起動しているか確認してください（ブラウザ版は不可）。
- `clientId` が正しく設定されているか確認してください。
- 他人に見せたい場合は、Discord の **設定 → アクティビティのプライバシー →「現在のアクティビティをステータスメッセージとして表示」** を ON にします。
- 動作ログ `%APPDATA%\REAPER\reaper_discord_presence.log` を確認してください。

**画像が出ない / デフォルト画像になる**
- Art Asset のキーが `reaper`（小文字）か確認してください。アップロード直後は反映に数分かかります。

**勝手に「離席中」になる / 戻らない**
- しばらく操作しないと「離席中」表示になるのは仕様です（`awayAfterMs`）。無効にするなら `0`、長くするなら値を大きく。REAPER を操作すれば通常表示に戻ります。
- REAPER 終了後に消えないときは `staleAfterMs` を短くしてください。

## ログ

GUI サブシステムでビルドしているためコンソール出力はありません。代わりに `%APPDATA%\REAPER\reaper_discord_presence.log` に動作ログを書き出します。

## ライセンス

[MIT](LICENSE)
