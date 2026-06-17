# REAPER Discord Rich Presence (Node 不要 / Go 単体 exe)

> Show your REAPER session as a Discord Rich Presence — no Node.js, no user
> token, just a single static Go `.exe` plus one small REAPER Lua script.
> The version line mirrors REAPER's own title bar.

REAPER を起動するだけで、Discord のプロフィールに

```
Playing REAPER
REAPER v7.74 -Licensed for personal/small business use
Project: song.rpp / Playing
```

を独自画像つきで表示します。**Node.js / npm は一切不要**です。

## 何が動的に変わって、何が固定か

Discord の表示は3行 + 画像です。動的に変わるのは**下2行**です。

| 表示 | 中身 | 動的？ |
|------|------|--------|
| 1行目 `Playing REAPER` | `Playing`＝activity type、`REAPER`＝**Discord App 名（固定）** | ❌ App名は固定。動詞は Playing/Listening/Watching/Competing のみ切替可（任意文字列は不可） |
| 2行目 | REAPER のタイトルバー（`REAPER v7.74 -Licensed ...`）を実際に読んで表示 | ✅ バージョン/ライセンスが変われば自動追従 |
| 3行目 | `Project: <ファイル名> / <再生状態>` | ✅ プロジェクト・再生/停止/録音で変化 |
| 大画像 | Art Asset キー `reaper` の画像 | 固定（任意で差し替え可） |

つまり **「〜をプレイ中（Playing REAPER）」の "REAPER" 部分は Discord の Application 名そのもの**で、1つの App では動的に変えられません（変えたいなら App を複数用意して `clientId` を切り替えるしかない）。代わりに、バージョン・プロジェクト名・再生状態といった**動的情報は2〜3行目**に出ます。

## 構成

```
REAPER 起動
  └─ Scripts/__startup.lua
       └─ dofile  Scripts/reaper_discord_presence.lua
            ├─ REAPER の状態を JSON に書き出す（2秒ごと, ハートビート兼用）
            │     %APPDATA%\REAPER\reaper_discord_presence.json
            └─ Scripts/reaper-discord-presence.exe を1回だけ起動

reaper-discord-presence.exe（Go 製・単体 exe・常駐）
  ├─ 上記 JSON を監視
  ├─ Discord ローカル IPC (\\.\pipe\discord-ipc-N) へ接続
  ├─ Rich Presence を更新
  └─ JSON が一定時間更新されない＝REAPER 終了 → Presence を消す
```

- Lua は **状態の書き出しだけ**を担当。Discord には一切触れない。
- 送信役の常駐プロセスは Discord の仕様上どうしても必要（RPC over IPC はネイティブアプリ向けにローカル IPC で送る仕組み）。それを Go の単体 exe で最小化している。
- **ユーザートークン方式 / self-bot は使わない。** 公式の Rich Presence IPC のみ。

## 必要なもの

- Windows 10 / 11
- Discord デスクトップ版（起動していること）
- REAPER（リソースパス: `%APPDATA%\REAPER`）
- Discord Developer Portal で作成した Application 1つ

## セットアップ手順

### 1. Discord Developer Portal で Application を作る

1. https://discord.com/developers/applications を開く。
2. **New Application** → 名前を `REAPER` にする。
   - ここで付けた**名前がそのまま `Playing REAPER` の "REAPER" 部分**になる。
3. 左メニュー **General Information** の **Application ID** をコピーしておく（数字の長い ID）。
4. 左メニュー **Rich Presence → Art Assets** を開き、画像をアップロードする。
   - ここで付けた **Asset 名（キー）を `reaper` にする**（小文字）。これが大きい画像になる。
   - 推奨: 512×512 以上の PNG。
   - アップロード反映には数分かかることがある。

### 2. Application ID を設定する

ビルド／配置後、初回起動時に

```
%APPDATA%\REAPER\reaper_discord_presence_config.json
```

が自動生成されます。これを開き、`clientId` を手順1でコピーした Application ID に書き換えてください。

```json
{
  "clientId": "ここに Application ID",
  "largeImageKey": "reaper",
  "largeImageText": "REAPER",
  "pollIntervalMs": 2000,
  "staleAfterMs": 10000,
  "showProjectName": true,
  "showTransportState": true,
  "showElapsed": true
}
```

### 3. exe を入手（ダウンロード or ビルド）

**A. ダウンロード（簡単）:** [Releases](../../releases) からプリビルドの `reaper-discord-presence.exe` を入手。

**B. 自分でビルド:** [Go](https://go.dev/dl/) を入れて:

```powershell
cd reaper-discord-presence
./build.ps1
```

`reaper-discord-presence.exe` が生成されます（コンソール窓が出ない GUI サブシステム build・完全静的）。

### 4. 配置

以下を REAPER の Scripts フォルダへコピー:

```
%APPDATA%\REAPER\Scripts\reaper-discord-presence.exe
%APPDATA%\REAPER\Scripts\reaper_discord_presence.lua
```

そして `%APPDATA%\REAPER\Scripts\__startup.lua` の末尾に次の1行を追記（既存内容は消さない）:

```lua
dofile(reaper.GetResourcePath() .. "/Scripts/reaper_discord_presence.lua")
```

### 5. 動作確認

1. Discord デスクトップ版を起動。
2. REAPER を起動（または再起動）。
3. 数秒後、Discord のプロフィールに `Playing REAPER` が出る。
4. 再生 / 停止 / 録音、プロジェクト切替で表示が変わる。
5. REAPER を閉じると数秒後に Presence が消える。

## 設定項目

| キー | 意味 |
|------|------|
| `clientId` | Discord Application ID（必須） |
| `largeImageKey` | Art Asset のキー（`reaper`） |
| `largeImageText` | 画像ホバー時のテキスト |
| `pollIntervalMs` | JSON を確認する間隔（既定 2000） |
| `staleAfterMs` | この時間 JSON が更新されないと REAPER 終了とみなす（既定 10000） |
| `showProjectName` | プロジェクト名を表示するか |
| `showTransportState` | 再生/停止/録音状態を表示するか |
| `showElapsed` | 経過時間（for HH:MM）を表示するか |

## トラブルシューティング

- **何も出ない**
  - Discord の **設定 → アクティビティのプライバシー → 「現在のアクティビティをステータスメッセージとして表示」** がオンか確認。
  - Discord デスクトップ版が起動しているか（ブラウザ版は IPC 非対応）。
  - `clientId` が正しく入っているか。
  - ログを確認: `%APPDATA%\REAPER\reaper_discord_presence.log`
- **画像が出ない / デフォルト画像になる**
  - Art Asset のキーが `reaper`（小文字）か。アップロード直後は反映に数分かかる。
- **表示が消えない / REAPER 終了後も残る**
  - `staleAfterMs` を短くする。
- **二重に起動してしまう不安**
  - exe は単一インスタンスを保証しているので、Lua が毎回起動しても二重常駐しない。

## ログ

GUI サブシステムでビルドしているためコンソール出力はありません。代わりに

```
%APPDATA%\REAPER\reaper_discord_presence.log
```

に動作ログを書き出します。
