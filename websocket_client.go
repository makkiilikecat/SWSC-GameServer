package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// --- グローバル変数 ---

// conn は現在アクティブなWebSocket接続を保持します。
// connMutex は conn 変数へのアクセスを保護するためのミューテックスです。
var (
	conn      *websocket.Conn
	connMutex sync.Mutex
)

// --- 主要関数 ---

// ConnectWebSocket は設定されたWebSocketサーバーへの接続を試みます。
// 接続に成功すると、メッセージの読み取りループを開始し、
// 接続が切断されるかエラーが発生するまでブロックします。
// Returns:
//
//	error: 接続、メッセージ送受信、または切断処理中に発生したエラー。
//	       トークン拒否の場合は *websocket.CloseError (Code=1008) を返します。
func ConnectWebSocket() error {
	// HTTPヘッダーに認証トークンを設定
	header := http.Header{}
	header.Add("Authorization", "Bearer "+AuthToken) // config.go の AuthToken を使用

	// WebSocketダイアラーの設定
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment, // 環境変数のプロキシ設定を使用
		HandshakeTimeout: 45 * time.Second,          // 接続タイムアウト
	}

	log.Printf("[WebSocket] 接続試行中: %s", WsURL) // config.go の WsURL を使用
	connAttempt, resp, err := dialer.Dial(WsURL, header)

	// 接続エラーハンドリング
	if err != nil {
		// HTTPレスポンスがある場合 (認証失敗など)
		if resp != nil {
			log.Printf("[WebSocket] HTTPエラー応答: %d %s", resp.StatusCode, resp.Status)
			// 認証失敗 (401 Unauthorized) の場合
			if resp.StatusCode == http.StatusUnauthorized {
				log.Println("[WebSocket] 認証失敗。トークンを確認してください。")
				// 認証失敗を示す特別なエラーを返す (リトライ停止のため)
				return &websocket.CloseError{Code: TokenRejectedCode, Text: "認証失敗"}
			}
		}
		// その他の接続エラー
		return fmt.Errorf("WebSocket接続エラー: %w", err)
	}

	// 接続成功
	log.Println("[WebSocket] 接続成功！")

	// グローバル変数に接続を保存 (ミューテックスで保護)
	connMutex.Lock()
	conn = connAttempt
	connMutex.Unlock()

	// CloseハンドラとPingハンドラを設定
	conn.SetCloseHandler(handleClose)
	conn.SetPingHandler(handlePing)

	// 接続確立後、現在のサーバー状態を通知する syncStatus を送信
	err = sendSyncStatus()
	if err != nil {
		log.Printf("[WebSocket] エラー: syncStatus 送信失敗: %v", err)
		connAttempt.Close() // 送信失敗なら接続を切る
		return err
	}

	// メッセージ読み取りループを開始 (この関数はここでブロックされる)
	readMessages(connAttempt)

	// --- readMessages ループが終了した場合 (接続切断時) ---
	connMutex.Lock()
	conn = nil // グローバル変数をクリア
	connMutex.Unlock()
	log.Println("[WebSocket] 接続が切断されました。")

	// 接続終了時の後処理とエラー返却
	closeErr := connAttempt.Close() // 念のため閉じる試行
	if closeErr == nil {
		// readMessagesが正常終了し、Closeもエラーなしの場合
		return fmt.Errorf("接続が正常に閉じられました")
	}
	// readMessages内でのエラー、またはClose時のエラーを返す
	return closeErr
}

// --- WebSocketイベントハンドラ ---

// handleClose はWebSocket接続が閉じたときに呼び出されるハンドラです。
// Args:
//
//	code (int): クローズコード。
//	text (string): クローズ理由。
//
// Returns:
//
//	error: トークン拒否の場合はリトライ停止のため *websocket.CloseError を返します。
//	       それ以外は一般的なエラーを返します。
func handleClose(code int, text string) error {
	log.Printf("[WebSocket] 接続 Close: Code=%d, Reason=%s", code, text)
	// トークン拒否コード (1008) の場合
	if code == TokenRejectedCode {
		log.Println("[WebSocket] 認証トークンがサーバーに拒否されました。接続リトライを停止します。")
		// リトライを停止させるために、特定のCloseErrorを返す
		return &websocket.CloseError{Code: code, Text: text}
	}
	// その他の Close は通常のエラーとして扱う
	return fmt.Errorf("接続 Close: %d %s", code, text)
}

// handlePing はサーバーからPingメッセージを受信したときに呼び出されるハンドラです。
// Pongメッセージをサーバーに返します。
// Args:
//
//	appData (string): Pingメッセージに含まれるデータ。
//
// Returns:
//
//	error: Pongメッセージの送信に失敗した場合のエラー。
func handlePing(appData string) error {
	// 現在の接続を取得 (ミューテックスで保護)
	connMutex.Lock()
	currentConn := conn
	connMutex.Unlock()
	if currentConn == nil {
		return fmt.Errorf("Ping受信時に接続が存在しません")
	}

	// Pongメッセージを送信 (WriteControlを使用)
	// Pongには受信したPingと同じデータを含める
	err := currentConn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second)) // タイムアウト設定
	if err != nil {
		log.Printf("[WebSocket] Pong送信エラー: %v", err)
		return err
	}
	log.Println("[WebSocket] Ping受信、Pong送信完了。") // Pong送信ログ
	return nil
}

// --- メッセージ処理 ---

// readMessages はWebSocket接続からメッセージを継続的に読み取り、処理します。
// 接続が切断されるか、読み取りエラーが発生するとループを終了します。
// Args:
//
//	currentConn (*websocket.Conn): メッセージを読み取る対象のWebSocket接続。
func readMessages(currentConn *websocket.Conn) {
	defer log.Println("[WebSocket] メッセージ読み取りループ終了。")

	for {
		// メッセージの読み取り (ブロックする)
		messageType, message, err := currentConn.ReadMessage()
		if err != nil {
			// 読み取りエラー (接続切断など) が発生したらループを抜ける
			log.Printf("[WebSocket] メッセージ読み取りエラー: %v", err)
			// エラー発生時は ConnectWebSocket() 関数側で後処理される
			return
		}

		// テキストメッセージの場合のみ処理
		if messageType == websocket.TextMessage {
			var msg WsMessage // 汎用メッセージ構造体
			// JSONデコード試行
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Printf("[WebSocket] JSONデコード失敗: %v, 受信メッセージ: %s", err, string(message))
				// 不正なメッセージに対するエラー応答を試みる
				sendErrorResponse(msg.RequestID, fmt.Sprintf("不正なJSON形式です: %v", err))
				continue // 次のメッセージへ
			}

			log.Printf("[WebSocket] メッセージ受信: Type=%s, RequestID=%s", msg.Type, msg.RequestID)

			// メッセージタイプに応じて処理を振り分け (各処理はゴルーチンで非同期実行)
			switch msg.Type {
			case "startServer":
				// ゲームサーバー起動要求 -> process_manager へ処理委譲
				go handleStartServerProcess(msg.RequestID, msg.Payload) // process_manager.go の関数
			case "stopServer":
				// ゲームサーバー停止要求 -> process_manager へ処理委譲
				go handleStopServerProcess(msg.RequestID, msg.Payload) // process_manager.go の関数
			case "connected":
				// サーバーからの接続完了通知など (必要に応じて処理)
				log.Printf("[WebSocket] サーバーからの接続完了通知を受信: %s", string(msg.Payload))
			// 他にサーバーから受信するメッセージタイプがあればここに追加
			default:
				// 未知のメッセージタイプ
				log.Printf("[WebSocket] 未対応メッセージタイプ: %s", msg.Type)
				sendErrorResponse(msg.RequestID, fmt.Sprintf("未対応のメッセージタイプです: %s", msg.Type))
			}
		} else {
			// テキスト以外のメッセージ (バイナリなど) は現在未対応
			log.Printf("[WebSocket] 未対応メッセージフォーマット(バイナリ等): %d", messageType)
		}
	}
}

// --- メッセージ送信関数 ---

// sendSyncStatus は現在のサーバー状態 (実行中サーバーリスト、最大起動可能数) を
// WebSocketサーバーに通知します。接続確立直後に呼び出されます。
// Returns:
//
//	error: メッセージのエンコードまたは送信に失敗した場合のエラー。
func sendSyncStatus() error {
	runningServers := getRunningServerNames() // process_manager からリスト取得

	// 設定されたポート範囲から最大同時起動可能数を計算
	maxServers := 0
	if MaxPort >= MinPort { // ポート範囲が有効か確認 (config.go の値)
		maxServers = MaxPort - MinPort
	} else {
		log.Printf("[WebSocket] 警告: ポート範囲が無効なため (Min:%d, Max:%d)、最大サーバー数を0として送信します。", MinPort, MaxPort)
	}

	// 送信するペイロードを作成
	payload := SyncStatusPayload{
		RunningServers: runningServers,
		MaxServers:     maxServers,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		// ペイロード構造体の問題である可能性が高い
		log.Printf("[WebSocket] 致命的エラー: syncStatus ペイロードのエンコードに失敗しました: %v", err)
		return fmt.Errorf("syncStatus ペイロードのエンコード失敗: %w", err)
	}

	// WsMessage 構造体を作成して送信
	syncMsg := WsMessage{Type: "syncStatus", Payload: payloadBytes}
	log.Printf("[WebSocket] syncStatus 送信: 実行中=%d件, 最大数=%d", len(runningServers), maxServers)
	return sendMessage(syncMsg) // 汎用送信関数を呼び出し
}

// sendMessage は指定された WsMessage をJSONにエンコードし、
// 現在のWebSocket接続を通じて送信します。
// この関数は内部で connMutex を使用して conn へのアクセスを安全に行います。
// Args:
//
//	msg (WsMessage): 送信するメッセージデータ。
//
// Returns:
//
//	error: 接続が存在しない、エンコード失敗、または送信失敗の場合のエラー。
func sendMessage(msg WsMessage) error {
	// 現在の接続を安全に取得
	connMutex.Lock()
	currentConn := conn
	connMutex.Unlock()

	// 接続が存在しない場合はエラー
	if currentConn == nil {
		log.Printf("[WebSocket] 送信エラー: 接続が存在しません。メッセージタイプ: %s", msg.Type)
		return fmt.Errorf("接続が存在しません")
	}

	// メッセージをJSONバイト列にエンコード
	messageBytes, err := json.Marshal(msg)
	if err != nil {
		// メッセージ構造体側の問題
		log.Printf("[WebSocket] 送信メッセージのエンコード失敗 (Type: %s): %v", msg.Type, err)
		return fmt.Errorf("送信メッセージのエンコード失敗: %w", err)
	}

	// WebSocket接続にメッセージを書き込む
	// WriteMessage はスレッドセーフ（内部でロック）なので、ここでは connMutex 不要
	err = currentConn.WriteMessage(websocket.TextMessage, messageBytes)
	if err != nil {
		log.Printf("[WebSocket] メッセージ送信エラー (Type: %s): %v", msg.Type, err)
		// 送信エラーは接続が切れている可能性を示唆する
		return fmt.Errorf("メッセージ送信エラー: %w", err)
	}

	// 送信成功ログ (デバッグ時以外はコメントアウト推奨)
	// log.Printf("[WebSocket] メッセージ送信成功: Type=%s, RequestID=%s", msg.Type, msg.RequestID)
	return nil
}

// sendResponse は、Botからの要求に対する汎用的な応答 (主に stopServer など) を送信します。
// Args:
//
//	requestID (string): 応答対象の元のリクエストID。
//	success (bool): 操作が成功したかどうか。
//	message (string): 結果を示すメッセージ。
//	configData (string): (stopServer成功時) サーバー設定XML文字列。
//	needsConfirm (...interface{}): (stopServer時) プレイヤー確認が必要かどうかの情報 (bool, int)。
func sendResponse(requestID string, success bool, message string, configData string, needsConfirm ...interface{}) {
	// 応答ペイロードを作成
	payload := ResponsePayload{
		Success: success,
		Message: message,
		Config:  configData,
		// AssignedPort, FailedItemIDs は startServer 成功応答でのみ設定
	}

	// 可変長引数 needsConfirm の処理 (stopServerのプレイヤー確認用)
	if len(needsConfirm) >= 2 {
		if val, ok := needsConfirm[0].(bool); ok {
			payload.NeedsConfirmation = val
		}
		if val, ok := needsConfirm[1].(int); ok {
			payload.Players = val
		}
	}

	// ペイロードをJSONにエンコード
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] 応答ペイロードエンコード失敗 (ReqID: %s): %v", requestID, err)
		return // エラーログのみで復帰
	}

	// WsMessage を作成して送信
	respMsg := WsMessage{Type: "response", RequestID: requestID, Payload: payloadBytes}
	log.Printf("[WebSocket] 応答送信: ReqID=%s, Success=%v", requestID, success)
	sendMessage(respMsg)
}

// sendStartSuccessResponse は startServer 要求が正常に完了した場合の応答を送信します。
// 割り当てられたポート番号と、ワークショップダウンロードに失敗したアイテムIDリストを含みます。
// Args:
//
//	requestID (string): 応答対象の元のリクエストID。
//	message (string): 成功メッセージ。
//	assignedPort (int): ゲームサーバーに割り当てられたポート番号。
//	failedItemIDs ([]string): ワークショップダウンロードに失敗したアイテムIDのリスト (失敗がなければ空)。
func sendStartSuccessResponse(requestID string, message string, assignedPort int, failedItemIDs []string) {
	// 応答ペイロードを作成
	payload := ResponsePayload{
		Success:      true,         // 成功フラグ
		Message:      message,      // 成功メッセージ
		AssignedPort: assignedPort, // 割り当てポート
		// ★ ダウンロード失敗リストを設定 (空の場合 omitempty で省略される)
		FailedItemIDs: failedItemIDs,
	}

	// ペイロードをJSONにエンコード
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] 起動成功応答ペイロードエンコード失敗 (ReqID: %s): %v", requestID, err)
		return
	}

	// WsMessage を作成して送信
	respMsg := WsMessage{Type: "response", RequestID: requestID, Payload: payloadBytes}
	log.Printf("[WebSocket] 起動成功応答送信: ReqID=%s, Port=%d, FailedItems=%d", requestID, assignedPort, len(failedItemIDs))
	sendMessage(respMsg)
}

// sendStatusUpdate は、時間のかかる処理 (ワークショップダウンロードなど) の進捗状況をBotに通知します。
// Args:
//
//	requestID (string): 通知対象の元のリクエストID。
//	status (string): 進捗状況を示すステータスコード (例: "workshop_download_start")。
//	message (string): Botに表示するためのメッセージ。
func sendStatusUpdate(requestID string, status string, message string) {
	// 通知ペイロードを作成
	payload := StatusUpdatePayload{
		Status:  status,
		Message: message,
	}

	// ペイロードをJSONにエンコード
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] ステータス更新ペイロードエンコード失敗 (ReqID: %s, Status: %s): %v", requestID, status, err)
		return
	}

	// WsMessage を作成して送信
	statusMsg := WsMessage{Type: "statusUpdate", RequestID: requestID, Payload: payloadBytes}
	log.Printf("[WebSocket] ステータス更新送信: ReqID=%s, Status=%s", requestID, status)
	sendMessage(statusMsg)
}

// sendErrorResponse は、リクエスト処理中に予期せぬエラーが発生した場合などに、
// エラー情報をBotに通知します。
// Args:
//
//	requestID (string): エラーが発生した元のリクエストID (不明な場合は空文字列)。
//	errorMessage (string): 送信するエラーメッセージ。
func sendErrorResponse(requestID string, errorMessage string) {
	// requestID がない場合 (どのリクエストに対するエラーか不明な場合) は送信しない
	if requestID == "" {
		log.Printf("[WebSocket] エラー応答送信試行 (RequestIDなし): %s", errorMessage)
		return
	}

	// エラーペイロードを作成
	payload := ErrorResponsePayload{Message: errorMessage}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] エラー応答ペイロードエンコード失敗 (ReqID: %s): %v", requestID, err)
		return
	}

	// WsMessage を作成して送信
	respMsg := WsMessage{Type: "error", RequestID: requestID, Payload: payloadBytes}
	log.Printf("[WebSocket] エラー応答送信: ReqID=%s", requestID)
	sendMessage(respMsg)
}
