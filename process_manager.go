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
)

type RunningProcessInfo struct {
	Process *os.Process
	Port    int
}

// --- プロセス管理変数 (Mapの型を変更) ---
var (
	// runningProcs map[string]*os.Process // 古い型
	runningProcs map[string]RunningProcessInfo // ★ 新しい型: キー: 構成名, 値: プロセス情報(プロセスとポート)
	procsMutex   sync.Mutex
)

// プロセスマネージャー初期化
func InitializeProcessManager() {
	// runningProcs = make(map[string]*os.Process) // 古い型
	runningProcs = make(map[string]RunningProcessInfo) // ★ 新しい型で初期化
	initializePortManager()
	log.Println("[プロセス管理] プロセスマネージャーを初期化しました。")
}

// 起動中のサーバー構成名のリストを取得
func getRunningServerNames() []string {
	procsMutex.Lock()
	defer procsMutex.Unlock()
	names := make([]string, 0, len(runningProcs))
	for name := range runningProcs {
		names = append(names, name)
	}
	log.Printf("[プロセス管理] 現在実行中のサーバーリスト: %v", names)
	return names
}

/**
 * @brief startServer要求のコア処理 (プロセス起動、設定ファイル操作など)
 * Botからの startServer メッセージペイロードを受け取り、サーバー起動を試みる。
 * @param requestID string Botからのリクエストに対応するID (応答用)
 * @param payload json.RawMessage Botから送られた startServer のペイロード部分
 */
 func handleStartServerProcess(requestID string, payload json.RawMessage) {
	// --- 1. ペイロードの解析 ---
	var data StartServerPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		// ペイロードのJSON形式が不正な場合
		log.Printf("[プロセス管理][開始][エラー] startServerペイロードのデコード失敗: %v", err)
		// Botにエラー応答を返す
		sendErrorResponse(requestID, fmt.Sprintf("ペイロード解析失敗: %v", err))
		return
	}
	log.Printf("[プロセス管理][開始] 要求受信: 構成名='%s'", data.Name)

	// --- 2. 空きポートの検索と割り当て ---
	log.Printf("[プロセス管理][開始] 空きポートを検索中 (範囲: %d-%d)...", MinPort, MaxPort)
	assignedPort, err := findAvailablePort(MinPort, MaxPort) // port_manager.go の関数
	if err != nil {
		// 利用可能なポートが見つからない場合
		log.Printf("[プロセス管理][開始][エラー] 空きポートが見つかりません: %v", err)
		// Botにエラー応答を返す
		sendResponse(requestID, false, fmt.Sprintf("空きポート確保失敗: %v", err), "") // success: false
		return
	}
	log.Printf("[プロセス管理][開始] ポート %d を割り当てます。", assignedPort)

	// --- 3. 設定ファイル(XML)のポート番号更新 ---
	log.Printf("[プロセス管理][開始] 受け取ったXMLのポートを %d に更新します...", assignedPort)
	updatedXmlString, err := updateXmlPort(data.Config, assignedPort) // config_manager.go の関数
	if err != nil {
		// XMLの更新に失敗した場合 (パースエラーなど)
		log.Printf("[プロセス管理][開始][エラー] XML内のポート更新失敗: %v", err)
		// Botにエラー応答を返す
		sendResponse(requestID, false, fmt.Sprintf("設定ファイルのポート更新失敗: %v", err), "")
		// この時点ではポートは確保されていないので解放は不要
		return
	}
	log.Printf("[プロセス管理][開始] XMLポート更新完了。")
	// デバッグ用に更新後のXML内容を確認する場合 (長くなる可能性あり)
	log.Printf("[プロセス管理][開始] 更新後XML:\n%s", updatedXmlString)

	// --- 4. 更新された設定ファイルの保存 ---
	log.Printf("[プロセス管理][開始] 更新された設定ファイル '%s' を保存します...", data.Name)
	err = saveConfigFile(data.Name, updatedXmlString) // config_manager.go の関数
	if err != nil {
		// ファイルの保存に失敗した場合 (権限エラーなど)
		log.Printf("[プロセス管理][開始][エラー] 更新済み設定ファイルの保存失敗: %v", err)
		// Botにエラー応答を返す
		sendResponse(requestID, false, fmt.Sprintf("設定ファイルの保存失敗: %v", err), "")
		// この時点ではポートは確保されていないので解放は不要
		return
	}
	// saveConfigFile 内で成功ログは出力されるのでここでは省略可
	// log.Printf("[プロセス管理][開始] ポート %d を設定したファイルを保存しました。", assignedPort)

	// --- 5. ポートを使用中にマーク ---
	// この時点でポートが他のプロセスに確保された場合に備える
	if !assignPort(assignedPort) { // port_manager.go の関数
		// ポートのマークに失敗した場合 (ほぼ起こらないはずだが念のため)
		log.Printf("[プロセス管理][開始][エラー] ポート %d を使用中にマークできませんでした（競合の可能性）。", assignedPort)
		// Botにエラー応答を返す
		sendResponse(requestID, false, fmt.Sprintf("ポート %d の確保に失敗しました（競合発生）。", assignedPort), "")
		// 保存したファイルは残したままでも良いが、削除する選択肢もある
		// _ = os.RemoveAll(filepath.Join(configBaseDir, data.Name))
		return
	}

	// --- 6. 既存プロセスの停止 (念のため) ---
	// もし同じ構成名で古いプロセスが管理マップに残っていた場合のクリーンアップ
	log.Printf("[プロセス管理][開始] 既存プロセスがあれば停止を試みます: '%s'", data.Name)
	stopExistingProcess(data.Name) // 内部でポート解放も行う (修正済みの場合)

	// --- 7. ゲームサーバープロセスの起動 ---
	log.Printf("[プロセス管理][開始] ゲームサーバープロセス '%s' を起動します...", data.Name)
	configDir := filepath.Join(configBaseDir, data.Name) // サーバーに渡すディレクトリパス
	cmd, err := startServerProcess(data.Name, configDir) // 内部で os/exec を実行
	if err != nil {
		// プロセスの起動自体に失敗した場合
		log.Printf("[プロセス管理][開始][エラー] ゲームサーバープロセス '%s' の起動失敗: %v", data.Name, err)
		// Botにエラー応答を返す
		sendResponse(requestID, false, fmt.Sprintf("サーバープロセスの起動失敗: %v", err), "")
		// ★ 失敗したので割り当てたポートを解放する
		releasePort(assignedPort)
		// 作成した設定ディレクトリを削除する (任意)
		// _ = os.RemoveAll(configDir)
		return
	}
	// 起動に成功した場合
	log.Printf("[プロセス管理][開始] プロセス起動成功: '%s' (PID: %d)", data.Name, cmd.Process.Pid)

	// --- 8. 起動したプロセス情報とポート番号を管理マップに保存 ---
	procsMutex.Lock()
	runningProcs[data.Name] = RunningProcessInfo{
		Process: cmd.Process,
		Port:    assignedPort, // ポート番号も一緒に保存
	}
	procsMutex.Unlock()
	log.Printf("[プロセス管理][開始] 実行中プロセスマップに登録: '%s' (PID: %d, Port: %d)", data.Name, cmd.Process.Pid, assignedPort)

	// --- 9. Botに成功応答を送信 ---
	// 成功メッセージと割り当てたポート番号を返す
	sendStartSuccessResponse(requestID,
		fmt.Sprintf("サーバー '%s' の起動成功 (PID: %d)", data.Name, cmd.Process.Pid),
		assignedPort) // websocket_client.go の関数

	// --- 10. プロセス終了監視を開始 ---
	// 新しいゴルーチンでプロセスの終了を待ち受ける
	go waitForProcessExit(data.Name, cmd.Process, assignedPort) // ポート番号も渡す

	log.Printf("[プロセス管理][開始] 全ての処理完了: '%s'", data.Name)
}

// stopServer要求のプロセス関連処理
func handleStopServerProcess(requestID string, payload json.RawMessage) {
	var data StopServerPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		log.Printf("[プロセス管理][停止] エラー: stopServerペイロードのデコード失敗: %v", err)
		sendErrorResponse(requestID, fmt.Sprintf("Invalid stopServer payload: %v", err))
		return
	}

	log.Printf("[プロセス管理][停止] 要求受信: 構成名=%s, 確認済み=%v", data.Name, data.Confirmed)

	// --- ★ Stage 6: プレイヤー数確認 (常に 0 人とする) ---
	if !data.Confirmed {
		dummyPlayerCount := 1 // 常に0人
		log.Printf("[プロセス管理][停止] プレイヤー数確認 (Stage 6 ダミー): %d 人 (構成: %s)", dummyPlayerCount, data.Name)

		if dummyPlayerCount > 0 {
			log.Printf("[プロセス管理][停止] プレイヤー %d 人のため確認が必要です。応答を返します。", dummyPlayerCount)
			// Botに応答 (needsConfirmation: true, players: count)
			sendResponse(requestID, false, fmt.Sprintf("プレイヤーが %d 人います。", dummyPlayerCount), "", true, dummyPlayerCount)
			return // 確認が必要なためここで処理中断
		}
		
		log.Printf("[プロセス管理][停止] プレイヤーがいないため、停止処理を続行します。")
	} else {
		log.Printf("[プロセス管理][停止] 確認済みフラグのため、プレイヤー数確認をスキップします。")
	}

	procsMutex.Lock()
	processInfo, ok := runningProcs[data.Name] // RunningProcessInfo を取得
	if !ok {
		procsMutex.Unlock()
		log.Printf("[プロセス管理][停止] 停止対象プロセスなし: %s", data.Name)
		sendResponse(requestID, false, fmt.Sprintf("サーバー '%s' は実行されていません。", data.Name), "")
		return
	}
	delete(runningProcs, data.Name) // マップから削除
	procsMutex.Unlock()

	processToStop := processInfo.Process // プロセスを取得
	assignedPort := processInfo.Port    // ★ ポート番号を取得

	log.Printf("[プロセス管理][停止] プロセス停止開始: '%s' (PID: %d)", data.Name, processToStop.Pid)
	killErr := processToStop.Kill()
	if killErr != nil {
		log.Printf("[プロセス管理][停止] プロセスKill失敗 (PID: %d): %v", processToStop.Pid, killErr)
	}
	_, waitErr := processToStop.Wait()
	logProcessExit(processToStop.Pid, waitErr)
	log.Printf("[プロセス管理][停止] プロセス停止完了: '%s' (PID: %d)", data.Name, processToStop.Pid)

	if assignedPort != -1 { // ポート番号が取得できていれば
		releasePort(assignedPort) // port_manager.go
	} else {
		log.Printf("[プロセス管理][停止] 警告: サーバー '%s' のポート番号が不明なため解放できませんでした。", data.Name)
	}

	configDir := filepath.Join(configBaseDir, data.Name)
	configFilePath := filepath.Join(configDir, "server_config.xml")
	configContent, readErr := os.ReadFile(configFilePath)
	if readErr != nil {
		log.Printf("[プロセス管理][停止] エラー: 設定ファイル読み込み失敗 (%s): %v", configFilePath, readErr)
		sendResponse(requestID, true, fmt.Sprintf("サーバー '%s' を停止しましたが、設定ファイル読み込み失敗: %v", data.Name, readErr), "")
	} else {
		log.Printf("[プロセス管理][停止] 設定ファイル読み込み成功: %s", configFilePath)
		sendResponse(requestID, true, fmt.Sprintf("サーバー '%s' を停止し、設定ファイルを読み込みました。", data.Name), string(configContent))
	}

	removeAllErr := os.RemoveAll(configDir)
	if removeAllErr != nil {
		log.Printf("[プロセス管理][停止] エラー: 設定ディレクトリ削除失敗 (%s): %v", configDir, removeAllErr)
	} else {
		log.Printf("[プロセス管理][停止] 設定ディレクトリ削除成功: %s", configDir)
	}
	// --- 停止処理ここまで ---
}

// 既存プロセス停止ヘルパー (変更なし)
func stopExistingProcess(name string) {
	procsMutex.Lock()
	existingInfo, ok := runningProcs[name]
	if ok {
		log.Printf("[プロセス管理] 既存プロセス停止試行: '%s' (PID: %d, Port: %d)", name, existingInfo.Process.Pid, existingInfo.Port)
		delete(runningProcs, name)
		procsMutex.Unlock() // Unlock してから Kill/Wait/Release

		if err := existingInfo.Process.Kill(); err != nil {
			log.Printf("[プロセス管理] 既存プロセスKill失敗 (PID: %d): %v", existingInfo.Process.Pid, err)
		} else {
			_, waitErr := existingInfo.Process.Wait()
			logProcessExit(existingInfo.Process.Pid, waitErr)
			log.Printf("[プロセス管理] 既存プロセス停止完了 (PID: %d)", existingInfo.Process.Pid)
		}
		
		// ★ ポート解放
		if existingInfo.Port != -1 {
			releasePort(existingInfo.Port)
		} else {
			log.Printf("[プロセス管理] 警告: 停止した既存プロセス '%s' のポート番号が不明でした。", name)
		}
	} else {
		procsMutex.Unlock()
	}
}

// サーバープロセス起動ヘルパー (変更なし)
func startServerProcess(name, configDir string) (*exec.Cmd, error) {
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("設定ディレクトリの絶対パス取得失敗: %w", err)
	}
	args := []string{"+server_dir", absConfigDir}
	cmd := exec.Command(ServerExePath, args...)
	cmd.Dir = filepath.Dir(ServerExePath)
	log.Printf("[プロセス管理] 実行コマンド: %s %v (WD: %s)", ServerExePath, args, cmd.Dir)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("プロセス開始失敗: %w", err)
	}
	return cmd, nil
}

// プロセス終了待機ゴルーチン
func waitForProcessExit(name string, process *os.Process, assignedPort int) {
	pid := process.Pid
	log.Printf("[プロセス管理] サーバー監視開始: '%s' (PID: %d, Port: %d)", name, pid, assignedPort)
	_, waitErr := process.Wait() // プロセス終了までブロック

	// ★ 終了したプロセスがマップに残っているか確認してから削除
	// （stopServerProcess などで先に削除されている可能性があるため）
	procsMutex.Lock()
	shouldRestart := false
	if currentInfo, ok := runningProcs[name]; ok && currentInfo.Process.Pid == pid {
		// マップに存在 === 予期せぬ終了 (stopServer などで意図的に削除されていない)
		delete(runningProcs, name)
		log.Printf("[プロセス管理] 予期せず終了したプロセスをマップから削除: '%s' (PID: %d)", name, pid)
		shouldRestart = true
	} else {
		// マップに存在しない === 正常な停止処理 (stopServer など) か、既に処理済み
		log.Printf("[プロセス管理] プロセス '%s' (PID: %d) は既にマップから削除されています (正常停止または処理済み)。", name, pid)
	}
	procsMutex.Unlock()

	// 終了ログを出力
	logProcessExit(pid, waitErr)

	// --- ★ Stage 6: 再起動処理 ---
	// マップに存在し、かつエラーで終了した場合のみ再起動を試みる
	if shouldRestart {
		// 1. クラッシュ検出イベント送信
		log.Printf("[プロセス管理][再起動] クラッシュ検出: '%s' (PID: %d)。再起動試行イベントを送信します。", name, pid)

		var errMsg string // エラーメッセージを格納する変数を宣言
		if waitErr != nil {
			errMsg = waitErr.Error() // waitErrがnilでなければエラーメッセージを取得
		} else {
			errMsg = "" // waitErrがnilなら空文字列を設定
		}
		sendServerEvent(ServerCrashDetectedPayload{
			EventType:  "serverCrashDetected",
			ServerName: name,
			Pid:        pid,
			Error:      errMsg,
		})

		// 2. 再起動処理 (実際にプロセスを起動)
		log.Printf("[プロセス管理][再起動] サーバー '%s' の再起動を試みます...", name)
		configDir := filepath.Join(configBaseDir, name) // 設定ディレクトリを取得
		newCmd, startErr := startServerProcess(name, configDir) // 再起動試行

		var restartSuccess bool
		var restartMsg string
		var newPid int = -1

		if startErr == nil {
			// 再起動成功
			restartSuccess = true
			newPid = newCmd.Process.Pid
			restartMsg = fmt.Sprintf("サーバー '%s' の再起動に成功しました (新しいPID: %d)。", name, newPid)
			log.Printf("[プロセス管理][再起動] %s", restartMsg)

			// 新しいプロセスをマップに登録
			procsMutex.Lock()
			runningProcs[name] = RunningProcessInfo{
				Process: newCmd.Process,
				Port:    assignedPort, // ★ クラッシュ前と同じポート番号を再利用
			}
			procsMutex.Unlock()

			// 新しいプロセスの終了監視を開始
			go waitForProcessExit(name, newCmd.Process, assignedPort)

		} else {
			// 再起動失敗
			restartSuccess = false
			restartMsg = fmt.Sprintf("サーバー '%s' の再起動プロセス開始に失敗しました: %v", name, startErr)
			log.Printf("[プロセス管理][再起動] エラー: %s", restartMsg)
			// 失敗した場合、serverInstances からは Bot 側の同期処理 (Stage 3) または
			// タイムアウト処理 (Stage 5) で削除されることを期待する。
		}

		// 3. 再起動結果イベント送信
		sendServerEvent(ServerRestartResultPayload{
			EventType:  "serverRestartResult",
			ServerName: name,
			Success:    restartSuccess,
			Message:    restartMsg,
		})
	}
	// --- 再起動処理ここまで ---
}

// プロセス終了ログヘルパー (変更なし)
func logProcessExit(pid int, waitErr error) {
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			log.Printf("[プロセス管理] プロセス終了 (エラーあり): PID=%d, Error=%v, ExitCode=%d", pid, waitErr, exitErr.ExitCode())
		} else if strings.Contains(waitErr.Error(), "signal: killed") {
			log.Printf("[プロセス管理] プロセス終了 (Killシグナル): PID=%d", pid)
		} else {
			log.Printf("[プロセス管理] プロセス終了 (不明なエラー): PID=%d, Error=%v", pid, waitErr)
		}
	} else {
		log.Printf("[プロセス管理] プロセス正常終了: PID=%d", pid)
	}
}

// 汎用サーバーイベント送信関数 (変更なし)
func sendServerEvent(payload interface{}) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[プロセス管理][イベント] ペイロードエンコード失敗: %v", err)
		return
	}
	eventMsg := WsMessage{Type: "serverEvent", Payload: payloadBytes}
	eventType := getEventType(payload) // イベントタイプ取得
	log.Printf("[プロセス管理][イベント] イベント送信: Type=%s", eventType)
	sendMessage(eventMsg)
}

// イベントペイロードから EventType を取得するヘルパー (変更なし)
func getEventType(payload interface{}) string {
	switch p := payload.(type) {
	case ServerCrashDetectedPayload:
		return p.EventType
	case ServerRestartResultPayload:
		return p.EventType
	default:
		// タイプアサーションを使ってフィールドを試みる (より安全)
		if plMap, ok := payload.(map[string]interface{}); ok {
			if et, ok := plMap["eventType"].(string); ok {
				return et
			}
		}
		return "unknown"
	}
}