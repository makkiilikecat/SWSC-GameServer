package main

import (
	"bytes"       // バッファ操作用
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"path/filepath" // パス結合用
	"regexp"        // Workshop IDの検証用
	"strings"       // 文字列操作用
)

// Workshop ID であることを検証するための正規表現 (数字のみで構成されるか)
var workshopIDRegex = regexp.MustCompile(`^\d+$`)

// --- XMLからWorkshop IDを抽出し、該当要素を削除する ---

// extractWorkshopIDsAndModifyXML は、入力されたXML文字列を解析し、
// <playlists> および <mods> タグ内の <path> 要素から Workshop ID を抽出します。
// 同時に、抽出元の <path> 要素を削除した新しいXML文字列を生成して返します。
// Args:
//   xmlString (string): 解析対象のXML文字列。
// Returns:
//   playlistIDs ([]string): 抽出されたプレイリストのWorkshop IDリスト。
//   modIDs ([]string): 抽出されたMODのWorkshop IDリスト。
//   modifiedXmlString (string): Workshop IDの <path> 要素が削除されたXML文字列。
//   err (error): 処理中にエラーが発生した場合のエラーオブジェクト。
func extractWorkshopIDsAndModifyXML(xmlString string) (playlistIDs []string, modIDs []string, modifiedXmlString string, err error) {
	decoder := xml.NewDecoder(strings.NewReader(xmlString))
	var output bytes.Buffer // 変更後のXMLを書き込むバッファ
	encoder := xml.NewEncoder(&output)
	encoder.Indent("", "  ") // ※ インデントは元の形式に依存するため、ここでは仮設定。必要に応じて調整。

	var inPlaylists, inMods bool // 現在 <playlists> または <mods> タグ内にいるかを示すフラグ
	var skipElement bool         // 現在の要素 (<path>...</path>) をスキップするかどうか
	var depth int                // XMLの階層深度 (要素スキップ判定用)

	log.Println("[XML管理] ワークショップIDの抽出と該当<path>要素の削除を開始します...")

	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break // XML終端
		}
		if tokenErr != nil {
			log.Printf("[XML管理] エラー: XMLトークンの読み取りに失敗しました: %v", tokenErr)
			return nil, nil, "", fmt.Errorf("XMLトークンの読み取りエラー: %w", tokenErr)
		}

		if skipElement {
			// スキップ対象要素内のトークンを処理
			switch token.(type) {
			case xml.StartElement:
				depth++ // ネストした要素があれば深度を増やす
			case xml.EndElement:
				depth-- // 要素の終わりで深度を減らす
			}
			if depth == 0 {
				skipElement = false // スキップ対象要素の終了タグに到達
				// log.Printf("[XML管理] 要素スキップ終了") // デバッグ用
			}
			continue // このトークンは出力しない
		}

		// トークン種別に応じて処理
		switch se := token.(type) {
		case xml.StartElement:
			isWorkshopPath := false // この <path> がワークショップIDを含むか
			currentTagName := se.Name.Local

			if currentTagName == "playlists" {
				inPlaylists = true
				inMods = false
			} else if currentTagName == "mods" {
				inPlaylists = false
				inMods = true
			} else if (inPlaylists || inMods) && currentTagName == "path" {
				// <playlists> または <mods> 内の <path> 要素
				for _, attr := range se.Attr {
					if attr.Name.Local == "path" {
						if workshopIDRegex.MatchString(attr.Value) {
							// path属性の値が数字のみ (Workshop ID) の場合
							isWorkshopPath = true
							id := attr.Value
							if inPlaylists {
								playlistIDs = append(playlistIDs, id)
								log.Printf("[XML管理] プレイリストID抽出: %s", id)
							} else { // inMods == true
								modIDs = append(modIDs, id)
								log.Printf("[XML管理] MOD ID抽出: %s", id)
							}
							break // path属性を見つけたらループを抜ける
						} else {
							// path属性が数字のみでない場合は、通常のパスとして扱う（削除しない）
							log.Printf("[XML管理] 通常パス検出（削除対象外）: %s 内の path=\"%s\"", currentTagName, attr.Value)
						}
					}
				}
			}

			if isWorkshopPath {
				// この <path> 要素はワークショップIDを含むため、全体をスキップする
				skipElement = true
				depth = 1 // スキップ開始
				// log.Printf("[XML管理] ワークショップ<path>要素スキップ開始") // デバッグ用
			} else {
				// スキップ対象外の開始タグは通常通りエンコード
				if err := encoder.EncodeToken(token); err != nil {
					log.Printf("[XML管理] エラー: XMLトークン '%s' のエンコードに失敗しました: %v", currentTagName, err)
					return nil, nil, "", fmt.Errorf("XMLトークンのエンコードエラー: %w", err)
				}
			}

		case xml.EndElement:
			currentTagName := se.Name.Local
			// 通常の終了タグはエンコード
			if err := encoder.EncodeToken(token); err != nil {
				log.Printf("[XML管理] エラー: XMLトークン '</%s>' のエンコードに失敗しました: %v", currentTagName, err)
				return nil, nil, "", fmt.Errorf("XMLトークンのエンコードエラー: %w", err)
			}

			// <playlists> または <mods> タグが閉じたらフラグをリセット
			if currentTagName == "playlists" {
				inPlaylists = false
			} else if currentTagName == "mods" {
				inMods = false
			}

		default:
			// その他のトークン (コメント、テキストデータなど) はそのままエンコード
			if err := encoder.EncodeToken(token); err != nil {
				// CharDataなど、特定のトークンタイプのエラーハンドリングが必要な場合がある
				log.Printf("[XML管理] エラー: XMLトークン (%T) のエンコードに失敗しました: %v", token, err)
				return nil, nil, "", fmt.Errorf("XMLトークン (%T) のエンコードエラー: %w", token, err)
			}
		}
	}

	// エンコーダーのバッファをフラッシュして書き込みを完了
	if err := encoder.Flush(); err != nil {
		log.Printf("[XML管理] エラー: XMLエンコーダーのフラッシュに失敗しました: %v", err)
		return nil, nil, "", fmt.Errorf("XMLエンコーダーのフラッシュエラー: %w", err)
	}

	modifiedXmlString = output.String()
	log.Printf("[XML管理] ID抽出と要素削除完了。抽出プレイリストID数: %d, 抽出MOD ID数: %d", len(playlistIDs), len(modIDs))
	// log.Printf("[XML管理] 変更後XML(一部):\n%s", modifiedXmlString[:min(500, len(modifiedXmlString))]) // デバッグ用

	return playlistIDs, modIDs, modifiedXmlString, nil
}

// --- ダウンロード成功したWorkshopアイテムのパスをXMLに追加する ---

// addWorkshopPathsToXML は、入力されたXML文字列 (既にIDパスが削除されている想定) に対して、
// ダウンロードに成功したプレイリストとMODのIDに対応する <path> 要素を追加します。
// Args:
//   xmlString (string): <path> 要素の追加対象となるXML文字列。
//   successfulPlaylistIDs ([]string): ダウンロードに成功したプレイリストIDのリスト。
//   successfulModIDs ([]string): ダウンロードに成功したMOD IDのリスト。
//   configDirAbsPath (string): このサーバー設定のディレクトリの絶対パス (MODパス生成用)。
// Returns:
//   finalXmlString (string): <path> 要素が追加された最終的なXML文字列。
//   err (error): 処理中にエラーが発生した場合のエラーオブジェクト。
func addWorkshopPathsToXML(xmlString string, successfulPlaylistIDs []string, successfulModIDs []string, configDirAbsPath string) (finalXmlString string, err error) {
	decoder := xml.NewDecoder(strings.NewReader(xmlString))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	encoder.Indent("", "  ") // ※ インデント設定

	log.Println("[XML管理] ダウンロード成功したアイテムの<path>要素をXMLに追加します...")
	log.Printf("[XML管理]   成功プレイリストID数: %d", len(successfulPlaylistIDs))
	log.Printf("[XML管理]   成功MOD ID数: %d", len(successfulModIDs))
	log.Printf("[XML管理]   設定ディレクトリ絶対パス: %s", configDirAbsPath)


	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			log.Printf("[XML管理] エラー: XMLトークンの読み取りに失敗しました: %v", tokenErr)
			return "", fmt.Errorf("XMLトークンの読み取りエラー: %w", tokenErr)
		}

		// 終了タグ </playlists> または </mods> の *直前* に新しい <path> 要素を挿入する
		if et, ok := token.(xml.EndElement); ok {
			if et.Name.Local == "playlists" && len(successfulPlaylistIDs) > 0 {
				log.Printf("[XML管理] </playlists> を検出。成功したプレイリストパス %d 件を追加します。", len(successfulPlaylistIDs))
				// プレイリストの <path> 要素を追加
				for _, id := range successfulPlaylistIDs {
					// パス形式: /rom/data/workshop_missions/ID (スラッシュ区切り)
					playlistPath := "/rom/data/workshop_missions/" + id
					pathElement := xml.StartElement{
						Name: xml.Name{Local: "path"},
						Attr: []xml.Attr{{Name: xml.Name{Local: "path"}, Value: playlistPath}},
					}
					// <path> 開始タグ
					if err := encoder.EncodeToken(pathElement); err != nil {
						log.Printf("[XML管理] エラー: プレイリスト<path>開始タグのエンコードに失敗 (ID: %s): %v", id, err)
						return "", fmt.Errorf("プレイリスト<path>開始タグのエンコードエラー: %w", err)
					}
					// </path> 終了タグ (中身はないので即座に閉じる)
					if err := encoder.EncodeToken(pathElement.End()); err != nil {
						log.Printf("[XML管理] エラー: プレイリスト<path>終了タグのエンコードに失敗 (ID: %s): %v", id, err)
						return "", fmt.Errorf("プレイリスト<path>終了タグのエンコードエラー: %w", err)
					}
					log.Printf("[XML管理]   プレイリストパス追加: %s", playlistPath)
				}
			} else if et.Name.Local == "mods" && len(successfulModIDs) > 0 {
				log.Printf("[XML管理] </mods> を検出。成功したMODパス %d 件を追加します。", len(successfulModIDs))
				// MODの <path> 要素を追加
				for _, id := range successfulModIDs {
					// パス形式: <設定ディレクトリ絶対パス>\rom\data\workshop_mods\ID (バックスラッシュ区切り)
					// 注意: filepath.JoinはOS依存の区切り文字を使うため、最終的に置換が必要
					modPathTemp := filepath.Join(configDirAbsPath, "rom", "data", "workshop_mods", id)
					// Goのパス区切り文字('/')をWindowsのパス区切り文字('\')に置換
					modPathFinal := strings.ReplaceAll(modPathTemp, "/", "\\")
					// もしLinux環境などで実行していて、configDirAbsPath自体に'\'が含まれていない場合は
					// filepath.ToSlashで一度'/'に正規化してから置換するなどの工夫が必要かもしれない。
					// または、最初から文字列として結合する。
					// modPathFinal := configDirAbsPath + "\\rom\\data\\workshop_mods\\" + id // 文字列結合例

					pathElement := xml.StartElement{
						Name: xml.Name{Local: "path"},
						Attr: []xml.Attr{{Name: xml.Name{Local: "path"}, Value: modPathFinal}},
					}
					// <path> 開始タグ
					if err := encoder.EncodeToken(pathElement); err != nil {
						log.Printf("[XML管理] エラー: MOD<path>開始タグのエンコードに失敗 (ID: %s): %v", id, err)
						return "", fmt.Errorf("MOD<path>開始タグのエンコードエラー: %w", err)
					}
					// </path> 終了タグ
					if err := encoder.EncodeToken(pathElement.End()); err != nil {
						log.Printf("[XML管理] エラー: MOD<path>終了タグのエンコードに失敗 (ID: %s): %v", id, err)
						return "", fmt.Errorf("MOD<path>終了タグのエンコードエラー: %w", err)
					}
					log.Printf("[XML管理]   MODパス追加: %s", modPathFinal)
				}
			}
		}

		// 現在のトークンをエンコード
		if err := encoder.EncodeToken(token); err != nil {
			log.Printf("[XML管理] エラー: XMLトークン (%T) のエンコードに失敗しました: %v", token, err)
			return "", fmt.Errorf("XMLトークン (%T) のエンコードエラー: %w", token, err)
		}
	}

	if err := encoder.Flush(); err != nil {
		log.Printf("[XML管理] エラー: XMLエンコーダーのフラッシュに失敗しました: %v", err)
		return "", fmt.Errorf("XMLエンコーダーのフラッシュエラー: %w", err)
	}

	finalXmlString = output.String()
	log.Println("[XML管理] <path>要素の追加完了。")
	// log.Printf("[XML管理] 最終XML(一部):\n%s", finalXmlString[:min(500, len(finalXmlString))]) // デバッグ用

	return finalXmlString, nil
}

// min 関数の定義 (Go 1.21 未満の場合) - デバッグログ用
// func min(a, b int) int {
// 	if a < b {
// 		return a
// 	}
// 	return b
// }