package main

import (
	"log"      // ログ出力用
	"os"       // 環境変数アクセス、ファイル操作用
	"strconv"  // 文字列から数値への変換用
	"time"     // time.Duration (ReconnectDelay) の定義用

	// .env ファイルから環境変数を読み込むためのライブラリ (インストールが必要: go get github.com/joho/godotenv)
	"github.com/joho/godotenv"
)

// --- 定数定義 ---

const (
	// WebSocket再接続試行までの待機時間
	ReconnectDelay = 5 * time.Second
	// サーバー設定ファイル (server_config.xml) が格納されるベースディレクトリ
	configBaseDir = "./config"
	// WebSocketサーバーが認証トークンを拒否した際のクローズコード (RFC 6455 Policy Violation)
	TokenRejectedCode = 1008

	// 環境変数名を定数として定義 (タイプミス防止、管理容易化)
	wsURLEnvKey                    = "WS_URL"                         // WebSocketサーバーのURL
	serverExePathEnvKey            = "SERVER_EXE_PATH"                // ゲームサーバー実行ファイルのパス
	tokenEnvKey                    = "TOKEN"                          // WebSocket接続認証用トークン
	minPortEnvKey                  = "MIN_PORT"                       // 使用するポート番号の最小値
	maxPortEnvKey                  = "MAX_PORT"                       // 使用するポート番号の最大値
	workshopPlaylistsInstallDirEnvKey = "WORKSHOP_PLAYLISTS_INSTALL_DIR" // ワークショップのプレイリスト(アドオン)をインストールするディレクトリパス
	workshopModsInstallDirEnvKey      = "WORKSHOP_MODS_INSTALL_DIR"      // ワークショップのModをインストールするディレクトリパス
	steamCmdPathEnvKey             = "STEAMCMD_PATH"                  // SteamCMD実行ファイルのパス
	gameAppIDEnvKey                = "GAME_APPID"                     // 対象ゲームのSteam App ID
)

// --- グローバル設定変数 ---
// LoadConfig() によって環境変数から読み込まれた値が格納される

var (
	// WebSocketサーバーの接続先URL
	WsURL string
	// 起動するゲームサーバーの実行ファイルのフルパス
	ServerExePath string
	// WebSocket接続時に使用する認証トークン
	AuthToken string
	// ゲームサーバーが使用するポート番号の範囲 (最小値)
	MinPort int
	// ゲームサーバーが使用するポート番号の範囲 (最大値)
	MaxPort int
	// SteamCMDがワークショップのプレイリストを配置するディレクトリ
	WorkshopPlaylistsInstallDir string
	// SteamCMDがワークショップのMODを配置するディレクトリ
	WorkshopModsInstallDir string
	// SteamCMDの実行ファイルのフルパス
	SteamCmdPath string
	// SteamCMDがワークショップアイテムをダウンロードする対象のゲームApp ID
	GameAppID string
)

// LoadConfig は、アプリケーション起動時に環境変数から設定値を読み込み、検証する関数。
// 必須の設定値が不足しているか、形式が無効な場合は log.Fatalf でプログラムを終了させる。
func LoadConfig() {
	// 1. .env ファイルの読み込み試行
	// カレントディレクトリに .env ファイルがあれば、その内容を環境変数として読み込む。
	// ファイルが存在しなくてもエラーにはせず、環境変数が直接設定されていればそちらを優先する。
	err := godotenv.Load()
	if err != nil {
		// .env ファイルが見つからない場合は通常動作なので、エラーログは出さない。
		// 読み込み自体に失敗した場合（例: フォーマットエラー）はログを出しても良いが、必須ではない。
		// log.Printf("[設定] .envファイルの読み込み中にエラーが発生しました (無視されます): %v", err)
	}
	log.Println("[設定] 環境変数を読み込み、検証します...")

	// 2. 各設定値を環境変数から読み込み、必須チェックと型検証を行う

	// WebSocket URL の読み込みと必須チェック
	WsURL = os.Getenv(wsURLEnvKey)
	if WsURL == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", wsURLEnvKey)
	}

	// サーバー実行ファイルパスの読み込みと必須チェック
	ServerExePath = os.Getenv(serverExePathEnvKey)
	if ServerExePath == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", serverExePathEnvKey)
	}
	// オプション: ファイルの存在確認 (必要に応じてコメント解除)
	// if _, err := os.Stat(ServerExePath); os.IsNotExist(err) {
	//	 log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' で指定されたファイル '%s' が見つかりません。", serverExePathEnvKey, ServerExePath)
	// }

	// 認証トークンの読み込みと必須チェック
	AuthToken = os.Getenv(tokenEnvKey)
	if AuthToken == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", tokenEnvKey)
	}

	// 最小ポート番号の読み込み、必須チェック、数値変換チェック
	minPortStr := os.Getenv(minPortEnvKey)
	if minPortStr == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", minPortEnvKey)
	}
	MinPort, err = strconv.Atoi(minPortStr) // 文字列を整数(int)に変換
	if err != nil {
		// Atoi でエラーが発生した場合 (文字列が数値でない場合)
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' ('%s') が有効な数値ではありません: %v", minPortEnvKey, minPortStr, err)
	}

	// 最大ポート番号の読み込み、必須チェック、数値変換チェック
	maxPortStr := os.Getenv(maxPortEnvKey)
	if maxPortStr == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", maxPortEnvKey)
	}
	MaxPort, err = strconv.Atoi(maxPortStr) // 文字列を整数(int)に変換
	if err != nil {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' ('%s') が有効な数値ではありません: %v", maxPortEnvKey, maxPortStr, err)
	}

	// ワークショップ プレイリスト ディレクトリの読み込みと必須チェック
	WorkshopPlaylistsInstallDir = os.Getenv(workshopPlaylistsInstallDirEnvKey)
	if WorkshopPlaylistsInstallDir == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", workshopPlaylistsInstallDirEnvKey)
	}
	// オプション: パスが有効かどうかの簡易チェック (例: 絶対パスか) (必要に応じてコメント解除)
	// if !filepath.IsAbs(WorkshopPlaylistsInstallDir) {
	//	 log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' ('%s') は絶対パスで指定する必要があります。", workshopPlaylistsInstallDirEnvKey, WorkshopPlaylistsInstallDir)
	// }

	// ワークショップ MOD ディレクトリの読み込みと必須チェック
	WorkshopModsInstallDir = os.Getenv(workshopModsInstallDirEnvKey)
	if WorkshopModsInstallDir == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", workshopModsInstallDirEnvKey)
	}
	// オプション: パスが有効かどうかの簡易チェック (必要に応じてコメント解除)
	// if !filepath.IsAbs(WorkshopModsInstallDir) {
	//	 log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' ('%s') は絶対パスで指定する必要があります。", workshopModsInstallDirEnvKey, WorkshopModsInstallDir)
	// }

	// SteamCMD パスの読み込みと必須チェック
	SteamCmdPath = os.Getenv(steamCmdPathEnvKey)
	if SteamCmdPath == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", steamCmdPathEnvKey)
	}
	// オプション: ファイルの存在確認 (必要に応じてコメント解除)
	// if _, err := os.Stat(SteamCmdPath); os.IsNotExist(err) {
	//	 log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' で指定されたファイル '%s' が見つかりません。", steamCmdPathEnvKey, SteamCmdPath)
	// }

	// ゲーム App ID の読み込み、必須チェック、数値変換チェック
	GameAppID = os.Getenv(gameAppIDEnvKey)
	if GameAppID == "" {
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' が設定されていません。", gameAppIDEnvKey)
	}
	if _, err := strconv.Atoi(GameAppID); err != nil { // App IDが数値形式であるかを確認
		log.Fatalf("[設定] 致命的エラー: 環境変数 '%s' ('%s') が有効な数値 (App ID) ではありません: %v", gameAppIDEnvKey, GameAppID, err)
	}

	// 3. ポート範囲の論理的な検証
	// 最小ポートが最大ポートより大きい場合は不正
	if MinPort > MaxPort {
		log.Fatalf("[設定] 致命的エラー: ポート範囲が無効です。最小ポート (%d) が最大ポート (%d) より大きいです。", MinPort, MaxPort)
	}
	// 一般的にウェルノウンポート(0-1023)は避け、最大ポート番号(65535)を超えないようにする
	if MinPort < 1024 || MaxPort > 65535 {
		// 警告ではなく致命的エラーとして扱う
		log.Fatalf("[設定] 致命的エラー: 指定されたポート範囲 (%d-%d) が不正です。1024から65535の間で指定してください。", MinPort, MaxPort)
	}

	// 4. 読み込み完了ログの出力
	// 読み込んだ設定値をコンソールに出力する (トークン自体はセキュリティのため出力しない)
	log.Println("[設定] 設定の読み込みと検証が完了しました:")
	log.Printf("  WebSocket URL (%s): %s", wsURLEnvKey, WsURL)
	log.Printf("  サーバー実行ファイルパス (%s): %s", serverExePathEnvKey, ServerExePath)
	log.Printf("  認証トークン (%s): 設定済み", tokenEnvKey) // 値自体は表示しない
	log.Printf("  ポート範囲 (%s-%s): %d - %d", minPortEnvKey, maxPortEnvKey, MinPort, MaxPort)
	log.Printf("  ワークショップ プレイリスト ディレクトリ (%s): %s", workshopPlaylistsInstallDirEnvKey, WorkshopPlaylistsInstallDir)
	log.Printf("  ワークショップ MOD ディレクトリ (%s): %s", workshopModsInstallDirEnvKey, WorkshopModsInstallDir)
	log.Printf("  SteamCMD パス (%s): %s", steamCmdPathEnvKey, SteamCmdPath)
	log.Printf("  ゲーム App ID (%s): %s", gameAppIDEnvKey, GameAppID)
}