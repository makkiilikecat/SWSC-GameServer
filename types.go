package main

import "encoding/json"

// --- WebSocket通信で使用するJSONメッセージ構造体定義 ---
// SWSC (Goクライアント) と Bot/WebSocketサーバー間で送受信されるデータ型を定義します。

// WsMessage は、全てのWebSocketメッセージの基本となる汎用構造体です。
type WsMessage struct {
	// Type は、メッセージの種類を示します (例: "startServer", "stopServer", "response", "syncStatus", "statusUpdate", "serverEvent", "error")。
	// このタイプに基づいて、受信側はペイロードを適切に解釈します。
	Type string `json:"type"`

	// RequestID は、Botからの要求(Request)に対応する応答(Response)や状況更新(StatusUpdate)を関連付けるための一意なIDです。
	// 要求メッセージ以外では省略されることがあります (omitempty)。
	RequestID string `json:"requestId,omitempty"`

	// Payload は、メッセージの実際のデータ部分です。
	// 型は json.RawMessage であり、具体的な内容は Type によって異なります。
	// 受信側で適切な構造体にアンマーシャルして使用します。
	Payload json.RawMessage `json:"payload"`
}

// StartServerPayload は、"startServer" 要求メッセージのペイロード構造体です。
// BotがSWSCにゲームサーバーの起動を要求する際に使用します。
type StartServerPayload struct {
	// Name は、起動するサーバーの一意な構成名 (例: "pvp_server", "creative_build") です。
	// 設定ファイルのディレクトリ名など、サーバーを識別するために使用されます。
	Name string `json:"name"`

	// Config は、サーバーの設定ファイル (server_config.xml) の内容を含むXML文字列です。
	// この時点では、<playlists> と <mods> タグにはWorkshop IDのみが含まれている想定です。
	// SWSC側でポート番号の割り当てと、Workshopアイテムのダウンロード/パス解決が行われます。
	Config string `json:"config"`
}

// StopServerPayload は、"stopServer" 要求メッセージのペイロード構造体です。
// BotがSWSCにゲームサーバーの停止を要求する際に使用します。
type StopServerPayload struct {
	// Name は、停止するサーバーの構成名です。
	Name string `json:"name"`

	// Confirmed は、プレイヤーがサーバーに接続している可能性がある場合に、停止を確認済みかどうかを示すフラグです。
	// true であれば、プレイヤー数に関わらず停止処理を進めます。
	// false の場合、SWSCはプレイヤー数を確認し、0人でなければ確認要求応答を返すことがあります (※現在の実装ではプレイヤー確認はダミー)。
	Confirmed bool `json:"confirmed"`
}

// SyncStatusPayload は、"syncStatus" メッセージのペイロード構造体です。
// SWSCがWebSocketサーバーに接続した際、自身の現在の状態を通知するために使用します。
type SyncStatusPayload struct {
	// RunningServers は、SWSCが現在管理している実行中のサーバー構成名のリストです。
	RunningServers []string `json:"runningServers"`

	// MaxServers は、SWSCの設定 (ポート範囲など) から計算された、同時に起動可能なサーバーの最大数です。
	MaxServers int `json:"maxServers"`
}

// ResponsePayload は、"response" メッセージのペイロード構造体です。
// startServer や stopServer などの要求に対する結果をBotに返すために使用します。
type ResponsePayload struct {
	// Success は、要求された操作が成功したかどうかを示します (true: 成功, false: 失敗)。
	Success bool `json:"success"`

	// Message は、操作結果に関する人間可読なメッセージです (例: "サーバー 'test' の起動に成功しました。", "エラー: ポートが確保できませんでした。")。
	Message string `json:"message"`

	// Config は、stopServer が成功した場合に、停止したサーバーの最終的な設定ファイル (server_config.xml) の内容を返します。
	// それ以外の場合は省略されます (omitempty)。
	Config string `json:"config,omitempty"`

	// AssignedPort は、startServer が成功した場合に、SWSCが割り当てたゲームサーバー用のポート番号です。
	// それ以外の場合は省略されます (omitempty)。
	AssignedPort int `json:"assignedPort,omitempty"`

	// FailedItemIDs は、startServer が成功した場合でも、ダウンロード/更新に失敗したワークショップアイテムのIDリストです。
	// 失敗がなかった場合は省略されます (omitempty)。Botはこの情報を使ってユーザーに通知できます。
	FailedItemIDs []string `json:"failedItemIDs,omitempty"` // ★ ステップ2で追加

	// --- stopServer時のプレイヤー確認用フィールド (現在はダミー実装) ---
	// NeedsConfirmation は、stopServer 要求時に Confirmed=false であり、かつプレイヤーが存在する場合に true となり、Botに追加確認を促します。
	NeedsConfirmation bool `json:"needsConfirmation,omitempty"`
	// Players は、NeedsConfirmation が true の場合に、検出されたプレイヤー数を示します。
	Players int `json:"players,omitempty"`
	// -------------------------------------------------------------
}

// StatusUpdatePayload は、"statusUpdate" メッセージのペイロード構造体です。 // ★ ステップ2で追加
// startServer 中のワークショップダウンロードなど、時間のかかる処理の進捗状況をBotに通知するために使用します。
type StatusUpdatePayload struct {
	// Status は、現在の処理状況を示す短い識別文字列です。
	// 例: "workshop_download_start", "workshop_download_running", "workshop_download_complete", "workshop_download_error"
	Status string `json:"status"`

	// Message は、現在の状況に関する人間可読なメッセージです (例: "ワークショップアイテムのダウンロードを開始しました...", "アイテム 5/10 件完了...")。
	Message string `json:"message"`
}

// ErrorResponsePayload は、"error" メッセージのペイロード構造体です。
// 特定のリクエストに対応しない一般的なエラー (例: 不正なメッセージ形式) をBotに通知するために使用します。
type ErrorResponsePayload struct {
	// Message は、発生したエラーの内容を示すメッセージです。
	Message string `json:"message"`
}

// --- サーバーイベント送信用ペイロード ---
// SWSC内部で発生したイベント (例: サーバークラッシュ) をBot/WebSocketサーバーに通知するための構造体です。
// WsMessage の Type が "serverEvent" となり、Payload に以下のいずれかの構造体が設定されます。

// ServerCrashDetectedPayload は、サーバープロセスの予期せぬ終了 (クラッシュ) を検出した際のイベントペイロードです。
type ServerCrashDetectedPayload struct {
	// EventType は、イベントの種類を示す固定文字列 "serverCrashDetected" です。
	EventType string `json:"eventType"`
	// ServerName は、クラッシュしたサーバーの構成名です。
	ServerName string `json:"serverName"`
	// Pid は、クラッシュしたプロセスのプロセスIDです。
	Pid int `json:"pid"`
	// Error は、プロセス終了時に取得されたエラーメッセージ (空の場合もあり) です。
	Error string `json:"error"`
}

// ServerRestartResultPayload は、クラッシュしたサーバーの自動再起動試行結果を通知する際のイベントペイロードです。
type ServerRestartResultPayload struct {
	// EventType は、イベントの種類を示す固定文字列 "serverRestartResult" です。
	EventType string `json:"eventType"`
	// ServerName は、再起動が試行されたサーバーの構成名です。
	ServerName string `json:"serverName"`
	// Success は、再起動プロセスを開始できたかどうかを示します (true: 成功, false: 失敗)。
	Success bool `json:"success"`
	// Message は、再起動試行の結果に関するメッセージです。
	Message string `json:"message"`
}

// ---------------------------------------------