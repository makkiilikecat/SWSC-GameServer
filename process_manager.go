package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"bufio"
)

// RunningProcessInfo は、実行中のゲームサーバープロセスとそのポート番号を保持する構造体です。
type RunningProcessInfo struct {
	Process *os.Process // 実行中のプロセスの情報
	Port    int         // そのプロセスが使用しているポート番号
}

// --- グローバル変数 ---

var (
	// runningProcs は、実行中のサーバープロセスを管理するマップです。
	// キー: サーバー構成名 (例: "test", "highway_lightng")
	// 値: RunningProcessInfo (プロセス情報とポート番号)
	runningProcs map[string]RunningProcessInfo
	// procsMutex は、runningProcs マップへの同時アクセスを保護するためのミューテックスです。
	procsMutex sync.Mutex
)

// InitializeProcessManager は、プロセスマネージャーを初期化します。
// runningProcs マップとポートマネージャーを初期化します。
func InitializeProcessManager() {
	runningProcs = make(map[string]RunningProcessInfo)
	initializePortManager() // port_manager.go の初期化関数を呼び出し
	log.Println("[プロセス管理] プロセスマネージャーを初期化しました。")
}

// getRunningServerNames は、現在実行中のサーバー構成名のリストを取得します。
// WebSocket での syncStatus 送信などに使用されます。
func getRunningServerNames() []string {
	procsMutex.Lock() // マップアクセス保護
	defer procsMutex.Unlock()
	names := make([]string, 0, len(runningProcs))
	for name := range runningProcs {
		names = append(names, name)
	}
	log.Printf("[プロセス管理] 現在実行中のサーバーリスト: %v", names)
	return names
}

// handleStartServerProcess は、WebSocket経由で受信した "startServer" 要求を処理するメイン関数です。
// ポート割り当て、設定ファイル処理、ワークショップダウンロード、サーバープロセス起動など、一連の処理を行います。
// 引数:
//   requestID (string): Botから送信された要求を一意に識別するID。応答や通知で使用します。
//   payload (json.RawMessage): "startServer" 要求のペイロード部分。StartServerPayload 構造体にデコードされます。
func handleStartServerProcess(requestID string, payload json.RawMessage) {
	// --- 1. ペイロード解析 ---
	// 受信したJSONペイロードを StartServerPayload 構造体にデコードします。
	var data StartServerPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		// デコード失敗はリクエスト形式が不正であることを示すため、エラー応答を返して終了します。
		log.Printf("[プロセス管理][開始:%s] エラー: startServerペイロードのデコード失敗: %v", requestID, err)
		sendErrorResponse(requestID, fmt.Sprintf("ペイロード解析失敗: %v", err)) // websocket_client.go
		return
	}
	log.Printf("[プロセス管理][開始:%s] 要求受信: 構成名='%s'", requestID, data.Name)

	// --- 2. 空きポートの検索 ---
	// 設定されたポート範囲内で利用可能なポートを探します。
	log.Printf("[プロセス管理][開始:%s] 空きポートを検索中 (範囲: %d-%d)...", requestID, MinPort, MaxPort) // MinPort, MaxPort は config.go で定義
	assignedPort, err := findAvailablePort(MinPort, MaxPort) // port_manager.go
	if err != nil {
		// 空きポートが見つからない場合はサーバーを起動できないため、エラー応答を返して終了します。
		log.Printf("[プロセス管理][開始:%s] エラー: 空きポートが見つかりません: %v", requestID, err)
		sendResponse(requestID, false, fmt.Sprintf("空きポート確保失敗: %v", err), "") // websocket_client.go
		return
	}
	log.Printf("[プロセス管理][開始:%s] ポート %d を使用予定。", requestID, assignedPort)

	// --- 3. 設定ファイル(XML)のポート番号更新 ---
	// Botから受け取ったXML文字列内のポート番号を、上で割り当てたポート番号に書き換えます。
	log.Printf("[プロセス管理][開始:%s] 受信したXMLのポートを %d に更新します...", requestID, assignedPort)
	xmlWithPort, err := updateXmlPort(data.Config, assignedPort) // config_manager.go
	if err != nil {
		// XMLのパースや更新に失敗した場合、設定ファイルが壊れている可能性があるため、エラー応答を返して終了します。
		log.Printf("[プロセス管理][開始:%s] エラー: XML内のポート更新失敗: %v", requestID, err)
		sendResponse(requestID, false, fmt.Sprintf("設定ファイルのポート更新失敗: %v", err), "")
		// この時点ではまだ assignPort() していないのでポート解放は不要です。
		return
	}
	log.Printf("[プロセス管理][開始:%s] XMLポート更新完了。", requestID)

	// --- 4. Workshop IDの抽出とXMLからの削除 ---
	// ポート更新後のXMLから、<playlists> および <mods> 内の Workshop ID (<path path="数字"/>) を抽出します。
	// 同時に、抽出元の <path> 要素をXMLから削除します。
	log.Printf("[プロセス管理][開始:%s] XMLからワークショップIDを抽出し、該当パスを削除します...", requestID)
	playlistIDs, modIDs, xmlWithIdsRemoved, err := extractWorkshopIDsAndModifyXML(xmlWithPort) // xml_manager.go
	if err != nil {
		// XMLのパースや操作に失敗した場合、エラー応答を返して終了します。
		log.Printf("[プロセス管理][開始:%s] エラー: XMLからのワークショップID抽出またはパス削除に失敗: %v", requestID, err)
		sendResponse(requestID, false, fmt.Sprintf("設定ファイルからのワークショップ情報抽出失敗: %v", err), "")
		return
	}
	log.Printf("[プロセス管理][開始:%s] 抽出したプレイリストID数: %d, MOD ID数: %d", requestID, len(playlistIDs), len(modIDs))

	// --- 5. Workshop アイテムのダウンロード/更新 ---
	var successfulPlaylistIDs, successfulModIDs, failedItemIDs []string
	xmlToSave := xmlWithIdsRemoved // デフォルトでは、IDが除去されたXMLを保存対象とします。

	// プレイリストIDまたはMOD IDが1つ以上抽出された場合のみ、ダウンロード処理を実行します。
	if len(playlistIDs) > 0 || len(modIDs) > 0 {
		log.Printf("[プロセス管理][開始:%s] ワークショップアイテムのダウンロード/更新処理を開始します。", requestID)
		// Botに進捗状況を通知します (ダウンロード開始)。
		sendStatusUpdate(requestID, "workshop_download_start", "ワークショップアイテムのダウンロード/更新を開始します...") // websocket_client.go

		// SteamCMDを実行してアイテムをダウンロード/更新し、成功したIDのリストを取得します。
		var steamCmdErr error
		successfulPlaylistIDs, successfulModIDs, steamCmdErr = DownloadWorkshopItems(
			playlistIDs,                 // 抽出したプレイリストID
			modIDs,                      // 抽出したMOD ID
			WorkshopPlaylistsInstallDir, // プレイリストのインストール先 (config.go)
			WorkshopModsInstallDir,      // MODのインストール先 (config.go)
			GameAppID,                 // ゲームのApp ID (config.go)
			SteamCmdPath,              // SteamCMDのパス (config.go)
		) // steamcmd_manager.go

		// SteamCMDの実行自体にエラーが発生した場合のログ出力 (パス不正、権限不足など)
		if steamCmdErr != nil {
			log.Printf("[プロセス管理][開始:%s] エラー: SteamCMDの実行中にエラーが発生しました: %v", requestID, steamCmdErr)
			// エラーがあっても一部アイテムは成功している可能性があるため、処理は続行しますが、Botに通知します。
			sendStatusUpdate(requestID, "workshop_download_error", fmt.Sprintf("SteamCMD実行エラー: %v", steamCmdErr))
			// 必要であればここで処理を中断し、エラー応答を返すことも可能です。
			// sendResponse(requestID, false, fmt.Sprintf("SteamCMD実行エラー: %v", steamCmdErr), "")
			// return
		}

		// 失敗したアイテムIDのリストを計算します。
		failedItemIDs = calculateFailedIDs(playlistIDs, modIDs, successfulPlaylistIDs, successfulModIDs)
		log.Printf("[プロセス管理][開始:%s] ワークショップアイテムのダウンロード/更新処理完了。成功: %d/%d, 失敗: %d",
			requestID, len(successfulPlaylistIDs)+len(successfulModIDs), len(playlistIDs)+len(modIDs), len(failedItemIDs))

		// Botに進捗状況を通知します (ダウンロード完了)。失敗件数もメッセージに含めます。
		completionMessage := fmt.Sprintf("ワークショップアイテムの処理完了。(成功: %d/%d)",
			len(successfulPlaylistIDs)+len(successfulModIDs), len(playlistIDs)+len(modIDs))
		if len(failedItemIDs) > 0 {
			completionMessage += fmt.Sprintf(" %d件のダウンロード/更新に失敗しました。", len(failedItemIDs))
		}
		sendStatusUpdate(requestID, "workshop_download_complete", completionMessage) // websocket_client.go

		// --- 6. 成功したアイテムのパスをXMLに追加 ---
		// ダウンロード/更新に成功したアイテムのパス情報を、ID除去後のXMLに追加します。
		log.Printf("[プロセス管理][開始:%s] 成功したワークショップアイテムのパスをXMLに追加します...", requestID)
		// MODの絶対パスを生成するために、設定ディレクトリの絶対パスが必要です。
		configDir := filepath.Join(configBaseDir, data.Name) // 例: ./config/test
		configDirAbs, pathErr := filepath.Abs(configDir)     // 例: C:\path\to\project\config\test
		if pathErr != nil {
			// 絶対パスの取得に失敗した場合、MODパスを正しく生成できないためエラーとします。
			log.Printf("[プロセス管理][開始:%s] エラー: 設定ディレクトリの絶対パス取得に失敗: %v", requestID, pathErr)
			sendResponse(requestID, false, fmt.Sprintf("設定ディレクトリのパス解決失敗: %v", pathErr), "")
			return
		}

		// XMLにパスを追加する処理を呼び出します。
		finalXmlString, xmlAddErr := addWorkshopPathsToXML(xmlWithIdsRemoved, successfulPlaylistIDs, successfulModIDs, configDirAbs) // xml_manager.go
		if xmlAddErr != nil {
			// パスの追加に失敗した場合、エラー応答を返して終了します。
			log.Printf("[プロセス管理][開始:%s] エラー: XMLへのワークショップパス追加に失敗: %v", requestID, xmlAddErr)
			sendResponse(requestID, false, fmt.Sprintf("設定ファイルへのワークショップ情報書き込み失敗: %v", xmlAddErr), "")
			return
		}
		xmlToSave = finalXmlString // 保存対象のXMLを、パスが追加された最終版に更新します。
		log.Printf("[プロセス管理][開始:%s] XMLへのワークショップパス追加完了。", requestID)

	} else {
		// ワークショップアイテムが指定されていなかった場合
		log.Printf("[プロセス管理][開始:%s] ワークショップアイテムは指定されていません。", requestID)
		failedItemIDs = []string{} // 失敗リストは空とします。
		// xmlToSave は xmlWithIdsRemoved (ポート更新済み、ID除去済みだが元々IDはなかった) のままです。
	}

	// --- 7. 最終的な設定ファイルの保存 ---
	// ポート番号が更新され、成功したワークショップアイテムのパスが追加されたXMLをファイルに保存します。
	log.Printf("[プロセス管理][開始:%s] 最終的な設定ファイル '%s' を保存します...", requestID, data.Name)
	// デバッグ用に保存内容を確認したい場合は以下のコメントを解除します。
	// log.Printf("[プロセス管理][開始:%s] 保存するXML:\n%s", requestID, xmlToSave)
	if err := saveConfigFile(data.Name, xmlToSave); err != nil { // config_manager.go
		// ファイルの保存に失敗した場合 (権限不足など)、エラー応答を返して終了します。
		log.Printf("[プロセス管理][開始:%s] エラー: 最終設定ファイルの保存失敗: %v", requestID, err)
		sendResponse(requestID, false, fmt.Sprintf("設定ファイルの保存失敗: %v", err), "")
		return
	}
	log.Printf("[プロセス管理][開始:%s] 最終設定ファイル保存成功。", requestID)


	// --- 8. ポートを使用中にマーク ---
	// これ以降、他のプロセスが同じポートを使用できないようにマークします。
	// ファイル保存後、プロセス起動直前に行うことで、ファイル準備失敗時にポートを無駄に確保しないようにします。
	if !assignPort(assignedPort) { // port_manager.go
		// ポートの確保に失敗した場合 (他のプロセスが先に確保したなど)、エラー応答を返します。
		log.Printf("[プロセス管理][開始:%s] エラー: ポート %d を使用中にマークできませんでした（競合の可能性）。", requestID, assignedPort)
		sendResponse(requestID, false, fmt.Sprintf("ポート %d の確保に失敗しました（競合発生）。", assignedPort), "")
		// 既に保存した設定ファイルとディレクトリを削除します。
		configDir := filepath.Join(configBaseDir, data.Name)
		_ = os.RemoveAll(configDir) // エラーは無視します（最悪残っても大きな問題ではない）。
		log.Printf("[プロセス管理][開始:%s] ポート確保失敗のため設定ディレクトリ '%s' を削除しました。", requestID, configDir)
		return
	}
	log.Printf("[プロセス管理][開始:%s] ポート %d を使用中にマークしました。", requestID, assignedPort)

	// --- 9. 既存プロセスの停止 (念のため) ---
	// 同じ構成名で古いプロセスが残っている場合に備えて、停止処理を試みます。
	log.Printf("[プロセス管理][開始:%s] 既存プロセスがあれば停止を試みます: '%s'", requestID, data.Name)
	stopExistingProcess(data.Name) // この関数内でポート解放も行われます (対象プロセスが見つかれば)。

	// --- 10. ゲームサーバープロセスの起動 ---
	// 準備が整ったので、実際にゲームサーバーの実行ファイルを開始します。
	log.Printf("[プロセス管理][開始:%s] ゲームサーバープロセス '%s' を起動します...", requestID, data.Name)
	configDir := filepath.Join(configBaseDir, data.Name) // プロセスに渡す設定ディレクトリのパス
	cmd, err := startServerProcess(data.Name, configDir) // ヘルパー関数内で os/exec を実行
	if err != nil {
		// プロセスの起動自体に失敗した場合 (実行ファイルが見つからない、権限不足など)。
		log.Printf("[プロセス管理][開始:%s] エラー: ゲームサーバープロセス '%s' の起動失敗: %v", requestID, data.Name, err)
		releasePort(assignedPort) // ★ 確保したポートを解放します。
		// 作成した設定ディレクトリも削除します。
		_ = os.RemoveAll(configDir)
		log.Printf("[プロセス管理][開始:%s] 起動失敗したため設定ディレクトリ '%s' を削除しました。", requestID, configDir)
		sendResponse(requestID, false, fmt.Sprintf("サーバープロセスの起動失敗: %v", err), "")
		return
	}
	// プロセス起動成功
	log.Printf("[プロセス管理][開始:%s] プロセス起動成功: '%s' (PID: %d)", requestID, data.Name, cmd.Process.Pid)

	// --- 11. 起動したプロセス情報とポート番号を管理マップに保存 ---
	// 起動したプロセスを管理対象に追加します。
	procsMutex.Lock() // マップアクセス保護
	runningProcs[data.Name] = RunningProcessInfo{
		Process: cmd.Process,  // プロセス情報
		Port:    assignedPort, // 使用ポート
	}
	procsMutex.Unlock()
	log.Printf("[プロセス管理][開始:%s] 実行中プロセスマップに登録: '%s' (PID: %d, Port: %d)", requestID, data.Name, cmd.Process.Pid, assignedPort)

	// --- 12. Botに成功応答を送信 ---
	// 全ての処理が完了したことをBotに通知します。
	successMessage := fmt.Sprintf("サーバー '%s' の起動処理完了 (PID: %d)", data.Name, cmd.Process.Pid)
	if len(failedItemIDs) > 0 {
		// ワークショップダウンロード失敗があった場合はメッセージに追記します。
		successMessage += fmt.Sprintf("。%d件のワークショップアイテムのダウンロード/更新に失敗しました。", len(failedItemIDs))
	}
	// 失敗リストもペイロードに含めて送信します (websocket_client.go 側で対応済み)。
	sendStartSuccessResponse(requestID, successMessage, assignedPort, failedItemIDs) // websocket_client.go

	// --- 13. プロセス終了監視を開始 ---
	// 起動したプロセスが予期せず終了しないか、別のゴルーチンで監視を開始します。
	go waitForProcessExit(data.Name, cmd.Process, assignedPort)

	// handleStartServerProcess 関数の処理はここまでで完了です。
	log.Printf("[プロセス管理][開始:%s] 全ての処理完了: '%s'", requestID, data.Name)
}

// calculateFailedIDs は、要求されたIDリストと成功したIDリストを比較し、
// 失敗した（成功リストに含まれていない）IDのリストを返します。
func calculateFailedIDs(requestedPlaylists, requestedMods, successfulPlaylists, successfulMods []string) []string {
	failedIDs := []string{}
	// 検索効率のため、成功したIDをマップに格納します。
	successMap := make(map[string]bool)

	for _, id := range successfulPlaylists {
		successMap[id] = true
	}
	for _, id := range successfulMods {
		successMap[id] = true
	}

	// 要求された各IDが成功マップに存在するかチェックします。
	for _, id := range requestedPlaylists {
		if !successMap[id] {
			failedIDs = append(failedIDs, id) // 存在しなければ失敗リストに追加
		}
	}
	for _, id := range requestedMods {
		if !successMap[id] {
			failedIDs = append(failedIDs, id) // 存在しなければ失敗リストに追加
		}
	}
	return failedIDs
}


// handleStopServerProcess は、WebSocket経由で受信した "stopServer" 要求を処理します。
// 指定されたサーバープロセスを停止し、関連リソース（ポート、設定ファイル）をクリーンアップします。
// 引数:
//   requestID (string): Botからの要求ID。
//   payload (json.RawMessage): "stopServer" 要求のペイロード。StopServerPayload にデコードされます。
func handleStopServerProcess(requestID string, payload json.RawMessage) {
	// --- ペイロード解析 ---
	var data StopServerPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		log.Printf("[プロセス管理][停止:%s] エラー: stopServerペイロードのデコード失敗: %v", requestID, err)
		sendErrorResponse(requestID, fmt.Sprintf("不正な停止要求ペイロード: %v", err))
		return
	}
	log.Printf("[プロセス管理][停止:%s] 要求受信: 構成名=%s, 確認済み=%v", requestID, data.Name, data.Confirmed)

	// --- プレイヤー数確認 (現状ダミー) ---
	// confirmed フラグが false の場合、プレイヤー数をチェックする想定 (現在は常に0人とする)
	if !data.Confirmed {
		dummyPlayerCount := 0 // ここで実際のプレイヤー数を取得するロジックが必要になる可能性がある
		log.Printf("[プロセス管理][停止:%s] プレイヤー数確認 (ダミー): %d 人 (構成: %s)", requestID, dummyPlayerCount, data.Name)

		if dummyPlayerCount > 0 {
			// プレイヤーがいる場合は、確認を求める応答を返し、処理を中断します。
			log.Printf("[プロセス管理][停止:%s] プレイヤー %d 人のため確認が必要です。応答を返します。", requestID, dummyPlayerCount)
			sendResponse(requestID, false, fmt.Sprintf("プレイヤーが %d 人います。", dummyPlayerCount), "", true, dummyPlayerCount) // needsConfirmation: true
			return
		}
		// プレイヤーがいない場合は処理を続行します。
		log.Printf("[プロセス管理][停止:%s] プレイヤーがいないため、停止処理を続行します。", requestID)
	} else {
		// confirmed フラグが true の場合は確認をスキップします。
		log.Printf("[プロセス管理][停止:%s] 確認済みフラグのため、プレイヤー数確認をスキップします。", requestID)
	}

	// --- プロセス停止処理 ---
	procsMutex.Lock() // マップアクセス保護
	// 停止対象のプロセス情報をマップから取得します。
	processInfo, ok := runningProcs[data.Name]
	if !ok {
		// プロセスが実行中でなければ、その旨を応答して終了します。
		procsMutex.Unlock()
		log.Printf("[プロセス管理][停止:%s] 停止対象プロセスなし: %s", requestID, data.Name)
		sendResponse(requestID, false, fmt.Sprintf("サーバー '%s' は実行されていません。", data.Name), "")
		return
	}
	// マップから削除します (Kill実行前に削除)。
	delete(runningProcs, data.Name)
	procsMutex.Unlock() // Kill実行前にアンロック

	processToStop := processInfo.Process // 停止するプロセス
	assignedPort := processInfo.Port    // 解放するポート

	log.Printf("[プロセス管理][停止:%s] プロセス停止開始: '%s' (PID: %d)", requestID, data.Name, processToStop.Pid)
	// プロセスにKillシグナルを送信します。
	killErr := processToStop.Kill()
	if killErr != nil {
		// すでにプロセスが終了している場合などにエラーが発生することがありますが、処理は続行します。
		log.Printf("[プロセス管理][停止:%s] プロセスKill失敗の可能性 (PID: %d): %v", requestID, processToStop.Pid, killErr)
	}
	// プロセスが完全に終了し、リソースが解放されるのを待ちます。
	_, waitErr := processToStop.Wait()
	// 終了ログを出力します (エラー情報を含む)。
	logProcessExit(processToStop.Pid, waitErr) // ヘルパー関数使用
	log.Printf("[プロセス管理][停止:%s] プロセス停止完了: '%s' (PID: %d)", requestID, data.Name, processToStop.Pid)

	// --- ポート解放 ---
	// プロセスが使用していたポートを解放します。
	if assignedPort != -1 { // ポート番号が記録されていれば
		releasePort(assignedPort) // port_manager.go
	} else {
		log.Printf("[プロセス管理][停止:%s] 警告: サーバー '%s' のポート番号が不明なため解放できませんでした。", requestID, data.Name)
	}

	// --- 設定ファイルの読み込みと削除 ---
	// 停止後に最終的な設定ファイルの内容を読み取り、Botに返却します。
	configDir := filepath.Join(configBaseDir, data.Name)
	configFilePath := filepath.Join(configDir, "server_config.xml")
	configContent, readErr := os.ReadFile(configFilePath) // ファイル読み込み

	// Botへの応答メッセージを作成
	var responseMsg string
	var responseConfig string
	if readErr != nil {
		// ファイル読み込みに失敗した場合
		log.Printf("[プロセス管理][停止:%s] エラー: 設定ファイル読み込み失敗 (%s): %v", requestID, configFilePath, readErr)
		responseMsg = fmt.Sprintf("サーバー '%s' を停止しましたが、設定ファイル読み込み失敗: %v", data.Name, readErr)
		responseConfig = "" // 設定内容は空
	} else {
		// ファイル読み込みに成功した場合
		log.Printf("[プロセス管理][停止:%s] 設定ファイル読み込み成功: %s", requestID, configFilePath)
		responseMsg = fmt.Sprintf("サーバー '%s' を停止し、設定ファイルを読み込みました。", data.Name)
		responseConfig = string(configContent) // 設定内容を文字列で渡す
	}
	// 停止自体は成功しているので success: true で応答します。
	sendResponse(requestID, true, responseMsg, responseConfig) // websocket_client.go

	// 使用済みの設定ディレクトリ全体を削除します。
	removeAllErr := os.RemoveAll(configDir)
	if removeAllErr != nil {
		// ディレクトリ削除失敗はログに記録するのみとします。
		log.Printf("[プロセス管理][停止:%s] エラー: 設定ディレクトリ削除失敗 (%s): %v", requestID, configDir, removeAllErr)
	} else {
		log.Printf("[プロセス管理][停止:%s] 設定ディレクトリ削除成功: %s", requestID, configDir)
	}
	// --- 停止処理ここまで ---
}


// stopExistingProcess は、指定された構成名のプロセスが実行中であれば停止し、ポートを解放します。
// startServerProcess の開始時に、古いプロセスが残っている場合に備えて呼び出されます。
func stopExistingProcess(name string) {
	procsMutex.Lock() // マップアクセス保護
	// マップから既存のプロセス情報を検索
	existingInfo, ok := runningProcs[name]
	if ok {
		// プロセスが見つかった場合
		log.Printf("[プロセス管理] 既存プロセス停止試行: '%s' (PID: %d, Port: %d)", name, existingInfo.Process.Pid, existingInfo.Port)
		// 先にマップから削除
		delete(runningProcs, name)
		procsMutex.Unlock() // Mutexを解放してからKill/Wait/Release (ブロック回避)

		processToStop := existingInfo.Process
		assignedPort := existingInfo.Port

		// プロセスをKill
		if err := processToStop.Kill(); err != nil {
			log.Printf("[プロセス管理] 既存プロセスKill失敗 (PID: %d): %v", processToStop.Pid, err)
		}
		// プロセス終了待機
		_, waitErr := processToStop.Wait()
		logProcessExit(processToStop.Pid, waitErr) // 終了ログ
		log.Printf("[プロセス管理] 既存プロセス停止完了 (PID: %d)", processToStop.Pid)

		// ポート解放
		if assignedPort != -1 {
			releasePort(assignedPort) // port_manager.go
		} else {
			log.Printf("[プロセス管理] 警告: 停止した既存プロセス '%s' のポート番号が不明でした。", name)
		}

	} else {
		// プロセスが見つからなければ、何もせずMutexを解放して終了
		procsMutex.Unlock()
	}
}

// startServerProcess は、指定された構成名と設定ディレクトリを使用して、
// ゲームサーバーの実行ファイル (ServerExePath) を起動します。
// 戻り値: 起動したプロセスの *exec.Cmd オブジェクト、またはエラー
func startServerProcess(name, configDir string) (*exec.Cmd, error) {
	// ゲームサーバーに渡す設定ディレクトリの絶対パスを取得します。
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("設定ディレクトリの絶対パス取得失敗: %w", err)
	}

	// ゲームサーバーの起動引数を設定します (例: "+server_dir C:\path\to\config\test")
	args := []string{"+server_dir", absConfigDir}

	// コマンドオブジェクトを作成します。
	cmd := exec.Command(ServerExePath, args...) // ServerExePath は config.go で読み込み済み

	// ゲームサーバーのワーキングディレクトリを実行ファイルのあるディレクトリに設定します。
	// (サーバーが相対パスでリソースを読み込む場合などに必要)
	cmd.Dir = filepath.Dir(ServerExePath)

	log.Printf("[プロセス管理] 実行コマンド: \"%s\" %v (作業ディレクトリ: %s)", ServerExePath, args, cmd.Dir)

	stdoutPipe, _ := cmd.StdoutPipe() // エラーハンドリング省略
	stderrPipe, _ := cmd.StderrPipe() // エラーハンドリング省略

	// 非同期でプロセスを開始します。
	if err := cmd.Start(); err != nil {
		// プロセスの開始自体に失敗した場合 (実行ファイルがない、権限不足など)
		return nil, fmt.Errorf("プロセス開始失敗: %w", err)
	}

	// stdout 監視ゴルーチン
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			log.Printf("[GameServer:%s][stdout] %s", name, scanner.Text())
		}
	}()
	// stderr 監視ゴルーチン
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Printf("[GameServer:%s][stderr] %s", name, scanner.Text())
		}
	}()

	// 成功した場合はコマンドオブジェクトを返します。
	return cmd, nil
}

// waitForProcessExit は、指定されたプロセスが終了するのを待機し、
// 予期せず終了した場合には再起動処理を試みるゴルーチンです。
func waitForProcessExit(name string, process *os.Process, assignedPort int) {
	pid := process.Pid
	log.Printf("[プロセス管理][監視:%s] サーバー監視開始 (PID: %d, Port: %d)", name, pid, assignedPort)

	// process.Wait() はプロセスが終了するまでブロックします。
	_, waitErr := process.Wait() // 終了時のエラー情報 (正常終了ならnil)

	// プロセス終了後、管理マップの状態を確認します。
	procsMutex.Lock() // マップアクセス保護
	processInfo, stillRunning := runningProcs[name]
	shouldRestart := false // 再起動フラグ

	// マップに同じ構成名で、かつ同じPIDのプロセス情報が存在するかチェック
	if stillRunning && processInfo.Process.Pid == pid {
		// 存在する場合 === stopServer などで意図的に停止されていない => 予期せぬ終了(クラッシュ)と判断
		delete(runningProcs, name) // マップから削除
		log.Printf("[プロセス管理][監視:%s] 予期せず終了したプロセスをマップから削除 (PID: %d)", name, pid)
		shouldRestart = true // 再起動フラグを立てる
	} else if stillRunning && processInfo.Process.Pid != pid {
        // マップには存在するがPIDが違う === 既に新しいプロセスで再起動されている可能性など (通常発生しにくい)
        log.Printf("[プロセス管理][監視:%s] 警告: 監視対象のPID(%d)とマップ内のPID(%d)が不一致です。再起動は行いません。", name, pid, processInfo.Process.Pid)
		shouldRestart = false
    } else {
		// マップに存在しない場合 === 正常な停止処理(stopServer等) または 既に他の要因で削除済み
		log.Printf("[プロセス管理][監視:%s] プロセスは既にマップから削除されています (PID: %d, 正常停止または処理済み)。再起動は行いません。", name, pid)
		shouldRestart = false
	}
	procsMutex.Unlock()

	// プロセスの終了ログを出力します。
	logProcessExit(pid, waitErr)

	// --- 再起動処理 ---
	// 予期せぬ終了と判断された場合のみ再起動を試みます。
	// shouldRestart = false
	if shouldRestart {
		log.Printf("[プロセス管理][再起動:%s] クラッシュ検出 (PID: %d)。再起動を試みます...", name, pid)

		// 1. クラッシュ検出イベントをWebSocketで送信します。
		var errMsg string
		if waitErr != nil {
			errMsg = waitErr.Error() // エラーがあればメッセージを取得
		}
		sendServerEvent(ServerCrashDetectedPayload{ // websocket_client.go
			EventType:  "serverCrashDetected",
			ServerName: name,
			Pid:        pid,
			Error:      errMsg,
		})

		// 2. ゲームサーバーの再起動を試みます (startServerProcessを再利用)。
		configDir := filepath.Join(configBaseDir, name) // 設定ディレクトリはそのまま使います。
		newCmd, startErr := startServerProcess(name, configDir)

		restartSuccess := false // 再起動成功フラグ
		restartMsg := ""      // 再起動結果メッセージ
		var newPid int = -1   // 新しいプロセスのPID

		if startErr == nil {
			// 再起動に成功した場合
			restartSuccess = true
			newPid = newCmd.Process.Pid
			restartMsg = fmt.Sprintf("サーバー '%s' の再起動に成功しました (新しいPID: %d)。", name, newPid)
			log.Printf("[プロセス管理][再起動:%s] %s", name, restartMsg)

			// 新しいプロセス情報を管理マップに登録します (ポートは同じものを再利用)。
			procsMutex.Lock()
			runningProcs[name] = RunningProcessInfo{
				Process: newCmd.Process,
				Port:    assignedPort, // 同じポートを再利用
			}
			procsMutex.Unlock()
			log.Printf("[プロセス管理][再起動:%s] 新プロセス情報をマップに登録 (PID: %d, Port: %d)", name, newPid, assignedPort)

			// ★重要: 再起動した新しいプロセスに対しても、終了監視を再帰的に開始します。
			go waitForProcessExit(name, newCmd.Process, assignedPort)

		} else {
			// 再起動に失敗した場合
			restartSuccess = false
			restartMsg = fmt.Sprintf("サーバー '%s' の再起動プロセス開始に失敗しました: %v", name, startErr)
			log.Printf("[プロセス管理][再起動:%s] エラー: %s", name, restartMsg)
			// 再起動に失敗した場合、クラッシュしたプロセスが掴んでいたポートが解放されないため、
			// ここで明示的に解放する必要があります。
			releasePort(assignedPort) // port_manager.go
			log.Printf("[プロセス管理][再起動:%s] 再起動失敗のためポート %d を解放しました。", name, assignedPort)
			// 設定ディレクトリは削除しません（手動での再起動や調査のため）。
		}

		// 3. 再起動結果イベントをWebSocketで送信します。
		sendServerEvent(ServerRestartResultPayload{ // websocket_client.go
			EventType:  "serverRestartResult",
			ServerName: name,
			Success:    restartSuccess, // 成功/失敗フラグ
			Message:    restartMsg,     // 結果メッセージ
		})

	}
	// waitForProcessExit ゴルーチンの終了
	log.Printf("[プロセス管理][監視:%s] 監視ゴルーチン終了 (PID: %d)", name, pid)
}


// logProcessExit は、プロセスの終了コードやエラー情報に基づいて詳細なログを出力します。
func logProcessExit(pid int, waitErr error) {
	exitCode := -1 // 不明または取得失敗時のデフォルト値
	errMsg := "正常終了" // デフォルトメッセージ

	if waitErr != nil {
		// エラーがある場合
		errMsg = waitErr.Error() // デフォルトはエラーオブジェクトの文字列表現
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			// ExitError型であれば、終了コードを取得できます。
			exitCode = exitErr.ExitCode()
			errMsg = fmt.Sprintf("エラー終了 (ExitCode: %d)", exitCode)
		} else if strings.Contains(waitErr.Error(), "signal: killed") {
			// Killシグナルで終了した場合 (Windowsでは異なる可能性あり、要確認)
			errMsg = "Killシグナルにより終了"
			// WindowsではExitCodeが-1や別の値になる可能性あり
			exitCode = -1 // 仮
		} else {
			// その他の種類のエラーの場合
			errMsg = fmt.Sprintf("不明なエラーで終了: %v", waitErr)
		}
		// エラー終了ログ
		log.Printf("[プロセス管理] プロセス終了: PID=%d, 状態=%s", pid, errMsg)
	} else {
		// waitErr が nil の場合は正常終了です。
		exitCode = 0
		errMsg = "正常終了"
		log.Printf("[プロセス管理] プロセス正常終了: PID=%d, ExitCode=%d", pid, exitCode)
	}
}

// sendServerEvent は、サーバーイベント（クラッシュ検出、再起動結果など）を
// WebSocketを通じてBot（またはサーバー）に送信するためのヘルパー関数です。
// 引数 payload は、各イベントに対応する構造体（例: ServerCrashDetectedPayload）です。
func sendServerEvent(payload interface{}) {
	// ペイロードをJSONにマーシャリングします。
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		// これはプログラム内部のエラー（構造体の定義ミスなど）の可能性が高いです。
		log.Printf("[プロセス管理][イベント] エラー: ペイロードのJSONエンコードに失敗しました: %v", err)
		return
	}
	// WebSocketメッセージを作成します (Type: "serverEvent")。
	eventMsg := WsMessage{Type: "serverEvent", Payload: payloadBytes}
	// イベントの種類をログ出力用に取得します。
	eventType := getEventType(payload)
	log.Printf("[プロセス管理][イベント] イベント送信: Type=%s", eventType)
	// sendMessage を使って実際に送信します。
	sendMessage(eventMsg) // websocket_client.go
}

// getEventType は、与えられたペイロードのインターフェースから、
// そのイベントタイプ ("serverCrashDetected"など) を示す文字列を取得します。
func getEventType(payload interface{}) string {
	// 型アサーションを使って具体的なイベントペイロードの型を判別します。
	switch p := payload.(type) {
	case ServerCrashDetectedPayload:
		return p.EventType // 構造体内の EventType フィールドを返す
	case ServerRestartResultPayload:
		return p.EventType // 構造体内の EventType フィールドを返す
	// 他のイベントタイプがあればここに追加
	default:
		// 未知の型の場合は "unknown" を返します。
		// リフレクションを使えばフィールド名で取得することも可能ですが、ここでは型で判定します。
		return "unknown"
	}
}