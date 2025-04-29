package main

import (
	"log"
	"time"

	"github.com/gorilla/websocket" // websocket をインポート
)

// --- 初期化 ---
func init() {
	// 設定読み込み (config.go の関数を呼び出し)
	LoadConfig() // ★ 大文字に変更
	// プロセスマネージャー初期化 (process_manager.go の関数を呼び出し)
	InitializeProcessManager() // ★ 大文字に変更
}

// --- メイン処理 ---
func main() {
	log.Println("[メイン] ゲームサーバー管理クライアントを開始します...")

	// 無限ループで接続を試行
	for {
		// WebSocket接続 (websocket_client.go の関数を呼び出し)
		err := ConnectWebSocket() // ★ 大文字に変更
		if err != nil {
			// トークン拒否のエラーチェックを websocket.CloseError を使うように修正
			if closeErr, ok := err.(*websocket.CloseError); ok && closeErr.Code == TokenRejectedCode { // ★ websocket.CloseError と大文字定数に変更
				log.Println("[メイン] トークンが拒否されたため、終了します。")
				break
			}
			// その他のエラー
			log.Printf("[メイン] 接続失敗または切断: %v", err)
		}

		// 再接続待機 (config.go の定数を使用)
		log.Printf("[メイン] %v 後に再接続します...", ReconnectDelay) // ★ 大文字に変更
		time.Sleep(ReconnectDelay) // ★ 大文字に変更
	}

	log.Println("[メイン] クライアントを終了します。")
}