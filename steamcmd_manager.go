package main

import (
	"bufio" // 標準出力/エラーの行ごとの読み取りのため
	"fmt"
	"io"
	"io/fs" // filepath.WalkDir で使うため
	"log"
	"os"            // ファイル操作 (削除、情報取得、ディレクトリ作成) のため
	"os/exec"       // SteamCMD を外部プロセスとして実行するため
	"path/filepath" // OSに依存しないパス操作のため
	"regexp"        // SteamCMDの出力から成功メッセージを正規表現で解析するため
	"strings"       // 文字列操作 (結合、置換) のため
	"sync"          // SteamCMDの出力監視ゴルーチンの完了を待つため
)

// SteamCMDの成功メッセージからWorkshop IDを抽出するための正規表現
// 例: "Success. Downloaded item 1234567890 to folder ..."
// グループ (\d+) でID部分をキャプチャする
var steamCmdSuccessRegex = regexp.MustCompile(`Success. Downloaded item (\d+)`)

// DownloadWorkshopItems は、SteamCMDを使用してワークショップアイテムをデフォルトパスにダウンロード/更新し、
// その後、設定で指定されたターゲットディレクトリに「既存を削除してからコピー」します。
// SteamCMDでのダウンロード成否と、その後の削除・コピー処理の成否を総合的に判断し、
// 最終的に処理が成功したアイテムのIDリストを返します。
//
// Args:
//
//	playlistIDs ([]string): ダウンロード/更新/コピー対象のプレイリストIDリスト。
//	modIDs ([]string): ダウンロード/更新/コピー対象のMOD IDリスト。
//	playlistDir (string): プレイリストの最終的な配置先ディレクトリパス (設定値)。
//	modDir (string): MODの最終的な配置先ディレクトリパス (設定値)。
//	gameAppID (string): 対象ゲームのSteam App ID (設定値)。
//	steamCmdPath (string): steamcmd.exe 実行ファイルへのフルパス (設定値)。
//
// Returns:
//
//	successfulPlaylistIDs ([]string): 正常に処理(ダウンロード/削除/コピー)が完了したプレイリストIDのリスト。
//	successfulModIDs ([]string): 正常に処理(ダウンロード/削除/コピー)が完了したMOD IDのリスト。
//	err (error): SteamCMDの起動失敗や出力読み取りエラーなど、処理を続行できない致命的なエラーが発生した場合のエラーオブジェクト。個別のアイテム処理失敗はエラーとして返さない。
func DownloadWorkshopItems(playlistIDs []string, modIDs []string, playlistDir string, modDir string, gameAppID string, steamCmdPath string) (successfulPlaylistIDs []string, successfulModIDs []string, err error) {

	// --- 初期チェック ---
	if len(playlistIDs) == 0 && len(modIDs) == 0 {
		log.Println("[SteamCMD] ダウンロード対象のワークショップアイテムはありません。")
		return []string{}, []string{}, nil // 対象がなければ正常終了
	}

	// --- 処理開始ログ ---
	log.Printf("[SteamCMD] ワークショップアイテムのダウンロード/更新を開始します...")
	log.Printf("[SteamCMD]   対象プレイリストID数: %d", len(playlistIDs))
	log.Printf("[SteamCMD]   対象MOD ID数: %d", len(modIDs))
	log.Printf("[SteamCMD]   ターゲット プレイリスト ディレクトリ: %s", playlistDir)
	log.Printf("[SteamCMD]   ターゲット MOD ディレクトリ: %s", modDir)
	log.Printf("[SteamCMD]   ゲーム App ID: %s", gameAppID)
	log.Printf("[SteamCMD]   SteamCMD パス: %s", steamCmdPath)

	// --- SteamCMDコマンド引数の構築 ---
	// force_install_dir を使わず、SteamCMDのデフォルト場所にダウンロードさせる
	args := []string{"+login", "anonymous"} // 匿名ログインを使用

	// ダウンロード対象の全IDを管理するマップ (後でプレイリストかMODか判定するため)
	allItems := make(map[string]string) // Key: ID, Value: "playlist" or "mod"

	// 全てのIDに対して workshop_download_item コマンドを追加
	for _, id := range playlistIDs {
		// validate オプション: 既に存在する場合は更新のみ、なければダウンロード
		args = append(args, "+workshop_download_item", gameAppID, id, "validate")
		allItems[id] = "playlist"
	}
	for _, id := range modIDs {
		args = append(args, "+workshop_download_item", gameAppID, id, "validate")
		allItems[id] = "mod"
	}

	args = append(args, "+quit") // 全てのダウンロードコマンドの後、SteamCMDを終了

	log.Printf("[SteamCMD] 実行コマンド: %s %s", steamCmdPath, strings.Join(args, " "))

	// --- SteamCMDの実行準備 ---
	cmd := exec.Command(steamCmdPath, args...)

	// 標準出力と標準エラー出力をパイプで取得
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[SteamCMD] エラー: 標準出力パイプの取得に失敗しました: %v", err)
		return nil, nil, fmt.Errorf("SteamCMDの標準出力パイプ取得エラー: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[SteamCMD] エラー: 標準エラー出力パイプの取得に失敗しました: %v", err)
		return nil, nil, fmt.Errorf("SteamCMDの標準エラー出力パイプ取得エラー: %w", err)
	}

	// downloadSuccessMap: SteamCMDのログ出力からダウンロード/更新成功を確認したIDを記録
	downloadSuccessMap := make(map[string]bool)
	var wg sync.WaitGroup // 出力監視ゴルーチンの完了待ち用
	var readErr error     // 出力読み取り中のエラーを保持する変数

	// --- SteamCMDの出力監視 (ゴルーチン) ---
	wg.Add(1)
	go func() { // 標準出力(stdout)監視ゴルーチン
		defer wg.Done() // ゴルーチン終了時にWaitGroupに通知
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() { // 1行ずつ読み取る
			line := scanner.Text()
			log.Printf("[SteamCMD][出力] %s", line) // SteamCMDの出力をログに記録

			// 成功メッセージを示す正規表現にマッチするか確認
			matches := steamCmdSuccessRegex.FindStringSubmatch(line)
			if len(matches) > 1 { // マッチし、ID部分がキャプチャできた場合
				successfulID := matches[1] // キャプチャしたIDを取得
				// 同じIDで複数回成功ログが出る場合があるので、初回のみ記録
				if _, exists := downloadSuccessMap[successfulID]; !exists {
					downloadSuccessMap[successfulID] = true // 成功マップに記録
					log.Printf("[SteamCMD] アイテム ID %s のダウンロード/更新成功をSteamCMDログから確認しました。", successfulID)
				}
			}
		}
		// スキャナーのエラーチェック (EOF以外)
		if err := scanner.Err(); err != nil && err != io.EOF {
			log.Printf("[SteamCMD] エラー: 標準出力の読み取り中にエラーが発生しました: %v", err)
			readErr = err // 読み取りエラーを記録
		}
	}()

	wg.Add(1)
	go func() { // 標準エラー出力(stderr)監視ゴルーチン
		defer wg.Done() // ゴルーチン終了時にWaitGroupに通知
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() { // 1行ずつ読み取る
			line := scanner.Text()
			log.Printf("[SteamCMD][エラー] %s", line) // エラー出力は常にログに記録
		}
		// スキャナーのエラーチェック (EOF以外)
		if err := scanner.Err(); err != nil && err != io.EOF {
			log.Printf("[SteamCMD] エラー: 標準エラー出力の読み取り中にエラーが発生しました: %v", err)
			// stdout側でエラーが発生していなければ、こちらのエラーを記録
			if readErr == nil {
				readErr = err
			}
		}
	}()

	// --- SteamCMDプロセスの開始と終了待機 ---
	if err := cmd.Start(); err != nil {
		log.Printf("[SteamCMD] エラー: SteamCMDプロセスの開始に失敗しました: %v", err)
		return nil, nil, fmt.Errorf("SteamCMDプロセスの開始エラー: %w", err)
	}
	log.Println("[SteamCMD] SteamCMDプロセスを開始しました。ダウンロード/更新処理の完了を待ちます...")

	waitErr := cmd.Wait() // SteamCMDプロセスが終了するまで待機

	wg.Wait() // 標準出力・標準エラー出力の読み取りゴルーチンが完了するまで待機

	// --- SteamCMD実行後のエラーチェック ---
	if readErr != nil {
		// 出力パイプの読み取りでエラーが発生した場合、成功したかの判断が不確実なため、処理を中断
		log.Printf("[SteamCMD] エラー: SteamCMDの出力読み取り中にエラーが発生したため、後続の処理を中断します: %v", readErr)
		return nil, nil, fmt.Errorf("SteamCMD出力読み取りエラー: %w", readErr)
	}
	if waitErr != nil {
		// SteamCMD自体がエラーコードで終了した場合 (例: ネットワークエラー、ディスク容量不足など)
		// ログには警告として記録するが、一部成功している可能性もあるため、後続のコピー処理は試行する
		log.Printf("[SteamCMD] 警告: SteamCMDプロセスがエラーで終了しました: %v。コピー処理を試行します。", waitErr)
	}
	log.Printf("[SteamCMD] SteamCMDプロセス終了。")

	// --- SteamCMDデフォルトダウンロードパスの決定 ---
	// steamcmd.exe と同じディレクトリにある steamapps/workshop/content/<AppID> を想定
	steamCmdDir := filepath.Dir(steamCmdPath)
	steamCmdContentBase := filepath.Join(steamCmdDir, "steamapps", "workshop", "content", gameAppID)
	log.Printf("[SteamCMD] デフォルトのSteamCMDコンテンツ基底パスを '%s' と判断しました。", steamCmdContentBase)
	// 実際にこのディレクトリが存在するか確認 (オプション)
	if _, statErr := os.Stat(steamCmdContentBase); os.IsNotExist(statErr) {
		log.Printf("[SteamCMD] 警告: SteamCMDのコンテンツ基底パス '%s' が見つかりません。SteamCMDが正常にアイテムをダウンロードできなかった可能性があります。", steamCmdContentBase)
		// 存在しない場合、コピー元がないため、成功リストは空で返る
	}

	// --- 削除＆コピー処理 ---
	// finalSuccessMap: 削除(該当する場合)とコピーの両方に成功したIDを記録
	finalSuccessMap := make(map[string]bool)
	log.Printf("[SteamCMD] ダウンロードされたアイテムの削除＆コピー処理を開始します...")

	// SteamCMDログで成功が確認されたIDのみを対象に処理
	for id, downloaded := range downloadSuccessMap {
		if !downloaded {
			continue // SteamCMDログで成功が確認できなかったものはスキップ
		}

		// アイテムタイプ ("playlist" or "mod") を取得
		itemType, ok := allItems[id]
		if !ok {
			log.Printf("[SteamCMD] 警告: 内部エラー。ダウンロード成功マップにあるID %s が元のアイテムリストに存在しません。", id)
			continue // 念のためスキップ
		}

		// コピー元パスとコピー先パスを決定
		var sourcePath string         // 実際にコピーするファイル/ディレクトリのパス
		var targetPath string         // Stormworksが期待する最終的なファイル/ディレクトリのパス
		var sourceExists bool = false // コピー元が存在するか確認するフラグ

		if itemType == "playlist" {
			// ★ プレイリストの場合、コピー元は <SteamCmdDefault>/<ID>/playlist ディレクトリ
			sourcePath = filepath.Join(steamCmdContentBase, id, "playlist")
			// ★ コピー先は <PlaylistTargetDir>/<ID> ディレクトリ
			targetPath = filepath.Join(playlistDir, id)
			log.Printf("[SteamCMD][%s:%s] 処理開始 (プレイリスト)...", itemType, id)
			log.Printf("[SteamCMD][%s:%s]   ソース(プレイリスト内容): %s", itemType, id, sourcePath)
			log.Printf("[SteamCMD][%s:%s]   ターゲット(ID名ディレクトリ): %s", itemType, id, targetPath)

			// ★ プレイリストのコピー元 (<ID>/playlist) が存在するか確認
			if _, statErr := os.Stat(sourcePath); os.IsNotExist(statErr) {
				log.Printf("[SteamCMD][%s:%s] エラー: 期待されるコピー元ディレクトリ '%s' が見つかりません。プレイリスト形式でないか、ダウンロードに失敗した可能性があります。スキップします。", itemType, id, sourcePath)
				continue // このIDは失敗扱い
			} else if statErr != nil {
				log.Printf("[SteamCMD][%s:%s] エラー: コピー元ディレクトリ '%s' の状態確認中にエラー: %v", itemType, id, sourcePath, statErr)
				continue // このIDは失敗扱い
			} else {
				sourceExists = true // コピー元が存在することを確認
			}

		} else if itemType == "mod" {
			// ★ MODの場合、コピー元は <SteamCmdDefault>/<ID> ディレクトリ全体
			sourcePath = filepath.Join(steamCmdContentBase, id)
			// ★ コピー先は <ModTargetDir>/<ID> ディレクトリ
			targetPath = filepath.Join(modDir, id)
			log.Printf("[SteamCMD][%s:%s] 処理開始 (MOD)...", itemType, id)
			log.Printf("[SteamCMD][%s:%s]   ソース(ID名ディレクトリ): %s", itemType, id, sourcePath)
			log.Printf("[SteamCMD][%s:%s]   ターゲット(ID名ディレクトリ): %s", itemType, id, targetPath)

			// ★ MODのコピー元 (<ID> ディレクトリ) が存在するか確認
			if _, statErr := os.Stat(sourcePath); os.IsNotExist(statErr) {
				log.Printf("[SteamCMD][%s:%s] エラー: 期待されるコピー元ディレクトリ '%s' が見つかりません。ダウンロードに失敗した可能性があります。スキップします。", itemType, id, sourcePath)
				continue // このIDは失敗扱い
			} else if statErr != nil {
				log.Printf("[SteamCMD][%s:%s] エラー: コピー元ディレクトリ '%s' の状態確認中にエラー: %v", itemType, id, sourcePath, statErr)
				continue // このIDは失敗扱い
			} else {
				sourceExists = true // コピー元が存在することを確認
			}
		} else {
			// allItems マップのキーは "playlist" か "mod" のはずなので、ここには到達しない想定
			log.Printf("[SteamCMD] 警告: 不明なアイテムタイプです。ID: %s", id)
			continue
		}

		// コピー元が存在する場合のみ削除とコピーを実行
		if sourceExists {
			// 1. 既存ターゲットディレクトリ削除
			//    コピー先 (<TargetDir>/<ID>) をまず削除する
			log.Printf("[SteamCMD][%s:%s]   既存ターゲットディレクトリ削除試行: %s", itemType, id, targetPath)
			removeErr := os.RemoveAll(targetPath)
			if removeErr != nil && !os.IsNotExist(removeErr) {
				// ディレクトリが存在しないエラー(os.IsNotExist)以外は問題あり (例: アクセス権限不足)
				log.Printf("[SteamCMD][%s:%s] エラー: 既存ターゲットディレクトリ '%s' の削除に失敗しました: %v", itemType, id, targetPath, removeErr)
				// 削除に失敗したらコピーに進めないため、このIDは失敗扱い
				continue // 次のIDへ
			}
			// 削除成功または元々存在しなかった場合のログ
			if removeErr == nil {
				log.Printf("[SteamCMD][%s:%s]   既存ターゲットディレクトリを削除しました。", itemType, id)
			} else {
				log.Printf("[SteamCMD][%s:%s]   既存ターゲットディレクトリは存在しませんでした。", itemType, id)
			}

			// 2. ディレクトリコピー
			//    copyDir ヘルパー関数を呼び出す
			log.Printf("[SteamCMD][%s:%s]   ディレクトリコピー試行 ('%s' -> '%s')...", itemType, id, sourcePath, targetPath)
			copyErr := copyDir(sourcePath, targetPath) // copyDir は変更不要
			if copyErr != nil {
				log.Printf("[SteamCMD][%s:%s] エラー: ディレクトリ '%s' から '%s' へのコピーに失敗しました: %v", itemType, id, sourcePath, targetPath, copyErr)
				// コピー失敗もこのIDは失敗扱い
				continue // 次のIDへ
			}
			log.Printf("[SteamCMD][%s:%s]   ディレクトリコピー成功。", itemType, id)

			// 削除（または不要）とコピーの両方が成功した場合のみ、最終成功マップに記録
			finalSuccessMap[id] = true
			log.Printf("[SteamCMD][%s:%s] 処理成功。", itemType, id)
		}
		// コピー元が存在しなかった場合は、ループの先頭で continue しているのでここには到達しない

	} // --- 削除＆コピー処理ループ終了 ---

	log.Printf("[SteamCMD] 削除＆コピー処理完了。")

	// --- 最終結果の集計 ---
	// 最終成功マップを基に、成功したプレイリストIDとMOD IDのリストを作成
	successfulPlaylistIDs = []string{}
	successfulModIDs = []string{}
	for id, success := range finalSuccessMap {
		if success {
			itemType := allItems[id] // allItems には必ず存在するはず
			if itemType == "playlist" {
				successfulPlaylistIDs = append(successfulPlaylistIDs, id)
			} else if itemType == "mod" { // mod であることを確認
				successfulModIDs = append(successfulModIDs, id)
			}
		}
	}

	// --- 最終結果ログ ---
	log.Printf("[SteamCMD] 最終結果:")
	log.Printf("[SteamCMD]   最終的に成功したプレイリストID数: %d / %d", len(successfulPlaylistIDs), len(playlistIDs))
	log.Printf("[SteamCMD]   最終的に成功したMOD ID数: %d / %d", len(successfulModIDs), len(modIDs))
	log.Println("[SteamCMD] ワークショップアイテム処理完了。")

	// 個別の削除/コピー失敗はエラーとして返さず、成功リストの差分で判断させる
	return successfulPlaylistIDs, successfulModIDs, nil
}

// copyDir は src ディレクトリの内容を dst ディレクトリに再帰的にコピーします。
// dst が存在しない場合は作成されます。dst が既に存在する場合、その中身は上書きされる可能性があります。
// 注意: dst 自体の削除は行わないため、呼び出し元で必要に応じて os.RemoveAll(dst) を実行してください。
//
// Args:
//
//	src (string): コピー元のディレクトリパス。
//	dst (string): コピー先のディレクトリパス。
//
// Returns:
//
//	error: コピー処理中にエラーが発生した場合のエラーオブジェクト。成功時は nil。
func copyDir(src, dst string) error {
	// 1. コピー元の情報を取得 (存在確認とディレクトリかどうかの確認)
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("コピー元 '%s' の情報取得エラー: %w", src, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("コピー元 '%s' はディレクトリではありません", src)
	}

	// 2. コピー先の親ディレクトリが存在することを確認し、コピー先ディレクトリ自体を作成
	// MkdirAll は親ディレクトリも必要に応じて作成し、既に存在してもエラーにならない
	// パーミッションはコピー元ディレクトリのものを引き継ぐ
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("コピー先ディレクトリ '%s' の作成エラー: %w", dst, err)
	}

	// 3. WalkDir を使用してコピー元のディレクトリを再帰的に探索
	//    filepath.SkipDir を使うことで、特定のディレクトリをスキップすることも可能
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		// WalkDir 自体からエラーが渡された場合 (例: 権限不足でアクセスできない)
		if walkErr != nil {
			return fmt.Errorf("'%s' の走査中にエラー発生: %w", path, walkErr)
		}

		// コピー先のパスを計算 (src からの相対パスを dst に結合)
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			// 通常、src 内のパスなのでエラーにならないはずだが念のため
			return fmt.Errorf("相対パスの計算エラー ('%s' に対して '%s'): %w", src, path, err)
		}
		dstPath := filepath.Join(dst, relPath)

		// 要素がディレクトリかファイルかで処理を分岐
		if d.IsDir() {
			// ディレクトリの場合:
			// コピー先に同じ名前のディレクトリを作成する。
			// MkdirAll なので、既に存在する場合や親ディレクトリがない場合も適切に処理される。
			// WalkDir はディレクトリに入る前にそのディレクトリ自身を処理するので、ここで作成する。
			// fs.DirEntry から直接 Mode() を取得できない場合があるので、再度 Stat するか、
			// srcInfo.Mode() を使う (サブディレクトリのパーミッションが異なる可能性は無視)。
			if err := os.MkdirAll(dstPath, srcInfo.Mode()); err != nil {
				return fmt.Errorf("コピー先サブディレクトリ '%s' の作成エラー: %w", dstPath, err)
			}
			// log.Printf("[コピー] ディレクトリ作成: %s", dstPath) // 詳細ログ
		} else {
			// ファイルの場合: 内容をコピーする
			// コピー元ファイルを開く
			srcFile, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("コピー元ファイル '%s' を開けません: %w", path, err)
			}
			// defer は return 後ではなく、この関数の呼び出しが終わるまで遅延されるため、
			// ループ内で使う場合は注意が必要だが、WalkDir の各コールバックは独立しているので問題ない。
			// ただし、多数のファイルがある場合、ファイルディスクリプタを大量に開く可能性があるので注意。
			defer srcFile.Close()

			// コピー先ファイルを作成 (同名ファイルがあれば上書きされる)
			// パーミッションはデフォルト (通常 0666 & umask) になる。必要なら srcInfo.Mode() を使う。
			dstFile, err := os.Create(dstPath)
			if err != nil {
				_ = srcFile.Close() // エラー時も Close を試みる
				return fmt.Errorf("コピー先ファイル '%s' を作成できません: %w", dstPath, err)
			}
			defer dstFile.Close()

			// io.Copy でファイル内容をコピー
			_, err = io.Copy(dstFile, srcFile)
			if err != nil {
				// Close 漏れを防ぐために Close を試みる
				_ = srcFile.Close()
				_ = dstFile.Close()
				return fmt.Errorf("ファイル '%s' から '%s' へのコピーエラー: %w", path, dstPath, err)
			}
			// log.Printf("[コピー] ファイルコピー: %s (%d バイト)", dstPath, bytesCopied) // 詳細ログ
		}
		return nil // この要素の処理が成功したら nil を返す
	}) // --- WalkDir 終了 ---

	// WalkDir 全体でエラーが発生した場合
	if err != nil {
		return fmt.Errorf("ディレクトリ '%s' から '%s' へのコピー処理全体でエラーが発生しました: %w", src, dst, err)
	}

	// 全ての処理が正常に完了
	return nil
}
