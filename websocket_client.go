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

// WebSocket接続変数
var (
	conn      *websocket.Conn
	connMutex sync.Mutex
)

// WebSocketサーバーに接続
func ConnectWebSocket() error {
	header := http.Header{}
	header.Add("Authorization", "Bearer "+AuthToken)
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 45 * time.Second,
	}
	log.Printf("[WebSocket] 接続試行中: %s", WsURL)
	connAttempt, resp, err := dialer.Dial(WsURL, header)
	if err != nil {
		if resp != nil {
			log.Printf("[WebSocket] HTTPエラー応答: %d %s", resp.StatusCode, resp.Status)
			if resp.StatusCode == http.StatusUnauthorized {
				log.Println("[WebSocket] 認証失敗。トークンを確認してください。")
				return &websocket.CloseError{Code: TokenRejectedCode, Text: "認証失敗"}
			}
		}
		return fmt.Errorf("WebSocket接続エラー: %w", err)
	}
	log.Println("[WebSocket] 接続成功！")
	connMutex.Lock()
	conn = connAttempt
	connMutex.Unlock()
	conn.SetCloseHandler(handleClose)
	conn.SetPingHandler(handlePing)

	err = sendSyncStatus() // Stage 3: syncStatus 送信
	if err != nil {
		log.Printf("[WebSocket] エラー: syncStatus 送信失敗: %v", err)
		connAttempt.Close()
		return err
	}

	readMessages(connAttempt) // メッセージ読み取りループ

	connMutex.Lock()
	conn = nil
	connMutex.Unlock()
	log.Println("[WebSocket] 接続が切断されました。")
	closeErr := connAttempt.Close()
	if closeErr == nil {
		return fmt.Errorf("接続が正常に閉じられました")
	}
	return closeErr
}

// Closeハンドラ
func handleClose(code int, text string) error {
	log.Printf("[WebSocket] 接続 Close: Code=%d, Reason=%s", code, text)
	if code == TokenRejectedCode {
		log.Println("[WebSocket] 認証トークンがサーバーに拒否されました。接続リトライを停止します。")
		return &websocket.CloseError{Code: code, Text: text}
	}
	return fmt.Errorf("接続 Close: %d %s", code, text)
}

// Pingハンドラ
func handlePing(appData string) error {
	connMutex.Lock()
	currentConn := conn
	connMutex.Unlock()
	if currentConn == nil {
		return fmt.Errorf("Ping受信時に接続が存在しません")
	}
	err := currentConn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	if err != nil {
		log.Println("[WebSocket] Pong送信エラー:", err)
		return err
	}
	return nil
}

// メッセージ読み取りループ
func readMessages(currentConn *websocket.Conn) {
	defer log.Println("[WebSocket] メッセージ読み取りループ終了。")
	for {
		messageType, message, err := currentConn.ReadMessage()
		if err != nil {
			log.Printf("[WebSocket] メッセージ読み取りエラー: %v", err)
			return
		}
		if messageType == websocket.TextMessage {
			var msg WsMessage
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Printf("[WebSocket] JSONデコード失敗: %v, message: %s", err, string(message))
				sendErrorResponse(msg.RequestID, fmt.Sprintf("Invalid JSON format: %v", err))
				continue
			}
			log.Printf("[WebSocket] メッセージ受信: Type=%s, RequestID=%s", msg.Type, msg.RequestID)
			switch msg.Type {
			case "startServer":
				go handleStartServerProcess(msg.RequestID, msg.Payload)
			case "stopServer":
				go handleStopServerProcess(msg.RequestID, msg.Payload)
			case "connected":
				log.Printf("[WebSocket] サーバーからの接続完了通知: %s", string(msg.Payload))
			default:
				log.Printf("[WebSocket] 未対応メッセージタイプ: %s", msg.Type)
				sendErrorResponse(msg.RequestID, fmt.Sprintf("Unknown message type: %s", msg.Type))
			}
		} else {
			log.Printf("[WebSocket] 未対応メッセージフォーマット: %d", messageType)
		}
	}
}

// ★ Stage 8: syncStatus メッセージ送信 (MaxServers を計算して追加)
func sendSyncStatus() error {
	runningServers := getRunningServerNames() // process_manager からリスト取得
	// ★ config.go の MinPort, MaxPort から最大数を計算
	maxServers := 0
	if MaxPort >= MinPort { // ポート範囲が有効か確認
		maxServers = MaxPort - MinPort + 1
	} else {
		log.Printf("[WebSocket] 警告: ポート範囲が無効なため (Min:%d, Max:%d)、最大サーバー数を0として送信します。", MinPort, MaxPort)
	}

	payload := SyncStatusPayload{
		RunningServers: runningServers,
		MaxServers:     maxServers, // ★ 計算した最大数を追加
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("syncStatus ペイロードのエンコード失敗: %w", err)
	}
	syncMsg := WsMessage{Type: "syncStatus", Payload: payloadBytes}
	log.Printf("[WebSocket] syncStatus 送信: Running=%v, MaxServers=%d", runningServers, maxServers)
	return sendMessage(syncMsg) // 汎用送信関数を使用
}

// 汎用メッセージ送信
func sendMessage(msg WsMessage) error {
	connMutex.Lock()
	currentConn := conn
	connMutex.Unlock()
	if currentConn == nil {
		log.Println("[WebSocket] 送信エラー: 接続がありません。")
		return fmt.Errorf("接続がありません")
	}
	messageBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[WebSocket] 送信メッセージのエンコード失敗: %v", err)
		return fmt.Errorf("送信メッセージのエンコード失敗: %w", err)
	}
	connMutex.Lock()
	defer connMutex.Unlock()
	err = currentConn.WriteMessage(websocket.TextMessage, messageBytes)
	if err != nil {
		log.Printf("[WebSocket] メッセージ送信エラー: %v", err)
		return fmt.Errorf("メッセージ送信エラー: %w", err)
	}
	return nil
}

// 応答メッセージ送信
func sendResponse(requestID string, success bool, message string, configData string, needsConfirm ...interface{}) {
	payload := ResponsePayload{
		Success: success,
		Message: message,
		Config:  configData,
	}
	if len(needsConfirm) >= 2 {
		if val, ok := needsConfirm[0].(bool); ok {
			payload.NeedsConfirmation = val
		}
		if val, ok := needsConfirm[1].(int); ok {
			payload.Players = val
		}
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] 応答ペイロードエンコード失敗 (ReqID: %s): %v", requestID, err)
		return
	}
	respMsg := WsMessage{Type: "response", RequestID: requestID, Payload: payloadBytes}
	sendMessage(respMsg)
}

// ★ Stage 7: 起動成功応答送信ヘルパー (ポート番号付き)
func sendStartSuccessResponse(requestID string, message string, assignedPort int) {
	payload := ResponsePayload{
		Success:      true,
		Message:      message,
		AssignedPort: assignedPort, // ★ ポート番号を設定
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] 起動成功応答ペイロードエンコード失敗 (ReqID: %s): %v", requestID, err)
		return
	}
	respMsg := WsMessage{Type: "response", RequestID: requestID, Payload: payloadBytes}
	sendMessage(respMsg)
}

// エラー応答メッセージ送信
func sendErrorResponse(requestID string, errorMessage string) {
	payload := ErrorResponsePayload{Message: errorMessage}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] エラー応答ペイロードエンコード失敗 (ReqID: %s): %v", requestID, err)
		return
	}
	respMsg := WsMessage{Type: "error", RequestID: requestID, Payload: payloadBytes}
	sendMessage(respMsg)
}